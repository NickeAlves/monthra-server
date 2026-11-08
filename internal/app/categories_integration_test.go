//go:build integration

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

// category is one entry of a GET /categories answer.
type category struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	DefaultKey *string `json:"defaultKey"`
}

// listCategories returns the categories of a GET /categories answer. It
// never calls t, so goroutines may run it.
func listCategories(client *http.Client, url string, auth map[string]string) ([]category, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %d", url, res.StatusCode)
	}
	var body struct {
		Categories []category `json:"categories"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Categories, nil
}

func mustList(t *testing.T, client *http.Client, url string, auth map[string]string) []category {
	t.Helper()
	list, err := listCategories(client, url, auth)
	require.NoError(t, err)
	return list
}

// byKey indexes the default categories of list by key.
func byKey(list []category) map[string]category {
	out := map[string]category{}
	for _, c := range list {
		if c.DefaultKey != nil {
			out[*c.DefaultKey] = c
		}
	}
	return out
}

func countCategories(t *testing.T, mongoURI, userID string) int64 {
	t.Helper()
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	oid, err := bson.ObjectIDFromHex(userID)
	require.NoError(t, err)
	n, err := client.Database("monthra").Collection(repomongo.CollCategories).CountDocuments(ctx, bson.D{{Key: "user_id", Value: oid}})
	require.NoError(t, err)
	return n
}

func TestCategoriesEndToEnd(t *testing.T) {
	api, mail, mongoURI, _ := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const ana, bob = "ana.categories@example.com", "bob.categories@example.com"
	password := strings.Repeat("correct horse ", 2)
	signUp(t, api, mail, ana, password)
	signUp(t, api, mail, bob, password)
	asAna := signIn(t, native, api, ana, password)
	asBob := signIn(t, native, api, bob, password)
	categories := api + "/api/v1/categories"

	assert.Equal(t, http.StatusUnauthorized, call(t, native, http.MethodGet, categories, nil, nil).status, "behind Authenticate")

	// The first read seeds the defaults, named in the stored locale (pt-BR).
	list := mustList(t, native, categories, asAna)
	require.Len(t, list, 22)
	assert.Equal(t, "Moradia", list[0].Name)
	assert.Equal(t, "housing", *list[0].DefaultKey)
	assert.Equal(t, "Outras receitas", list[21].Name)
	assert.Equal(t, list, mustList(t, native, categories, asAna), "seeded once")

	income := mustList(t, native, categories+"?kind=income", asAna)
	assert.Len(t, income, 6)
	for _, c := range income {
		assert.Equal(t, "income", c.Kind)
	}
	r := call(t, native, http.MethodGet, categories+"?kind=transfer", nil, asAna)
	assert.Equal(t, http.StatusUnprocessableEntity, r.status)

	// Names follow the stored locale until renamed.
	groceries := byKey(list)["groceries"]
	r = call(t, native, http.MethodPatch, categories+"/"+groceries.ID, map[string]any{"name": "Supermercado do bairro"}, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, "groceries", r.str("defaultKey"), "a renamed default keeps its key")
	r = call(t, native, http.MethodPatch, api+"/api/v1/me/preferences", map[string]any{"locale": "en-US"}, asAna)
	require.Equal(t, http.StatusOK, r.status)
	english := byKey(mustList(t, native, categories, asAna))
	assert.Equal(t, "Housing", english["housing"].Name)
	assert.Equal(t, "Salary", english["salary"].Name)
	assert.Equal(t, "Supermercado do bairro", english["groceries"].Name, "the owner's name in every language")
	r = call(t, native, http.MethodGet, categories+"/"+english["salary"].ID, nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, "Salary", r.str("name"))

	// Kind is immutable; icon and color are checked.
	r = call(t, native, http.MethodPatch, categories+"/"+groceries.ID, map[string]any{"kind": "income"}, asAna)
	assert.Equal(t, http.StatusBadRequest, r.status, "kind never changes")
	r = call(t, native, http.MethodPost, categories, map[string]any{"name": "Gym", "kind": "expense", "icon": "Dumbbell", "color": "#12345"}, asAna)
	require.Equal(t, http.StatusUnprocessableEntity, r.status)
	assert.Contains(t, r.body["errors"], map[string]any{"field": "icon", "code": "invalid"})
	assert.Contains(t, r.body["errors"], map[string]any{"field": "color", "code": "invalid"})

	// Create.
	r = call(t, native, http.MethodPost, categories, map[string]any{"name": "Gym", "kind": "expense", "icon": "dumbbell", "color": "#ff8800"}, asAna)
	require.Equal(t, http.StatusCreated, r.status)
	gymID := r.str("id")
	assert.Equal(t, "/api/v1/categories/"+gymID, r.header.Get("Location"))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	assert.Equal(t, "#FF8800", r.str("color"))
	assert.Nil(t, r.body["defaultKey"])
	list = mustList(t, native, categories, asAna)
	require.Len(t, list, 23)
	assert.Equal(t, gymID, list[22].ID, "the newest last")

	// A deleted default never comes back.
	housing := byKey(list)["housing"]
	assert.Equal(t, http.StatusNoContent, call(t, native, http.MethodDelete, categories+"/"+housing.ID, nil, asAna).status)
	assert.Equal(t, http.StatusNotFound, call(t, native, http.MethodDelete, categories+"/"+housing.ID, nil, asAna).status)
	list = mustList(t, native, categories, asAna)
	assert.Len(t, list, 22)
	assert.NotContains(t, byKey(list), "housing")

	// Another user never reaches Ana's categories.
	for _, tc := range []struct {
		method string
		body   any
	}{{http.MethodGet, nil}, {http.MethodPatch, map[string]any{"name": "Mine"}}, {http.MethodDelete, nil}} {
		r = call(t, native, tc.method, categories+"/"+gymID, tc.body, asBob)
		assert.Equal(t, http.StatusNotFound, r.status, tc.method)
		assert.Equal(t, "not_found", r.str("code"), tc.method)
	}
	assert.Equal(t, http.StatusNotFound, call(t, native, http.MethodGet, categories+"/not-an-id", nil, asAna).status)
	r = call(t, native, http.MethodGet, categories+"/"+gymID, nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, "Gym", r.str("name"), "untouched by Bob")

	// Bob's first reads run at the same time: every one sees the whole set,
	// and the defaults are stored once.
	var wg sync.WaitGroup
	lists := make([][]category, 10)
	errs := make([]error, len(lists))
	for i := range lists {
		wg.Go(func() { lists[i], errs[i] = listCategories(native, categories, asBob) })
	}
	wg.Wait()
	for i := range lists {
		require.NoError(t, errs[i])
		assert.Len(t, lists[i], 22, "request %d", i)
	}
	r = call(t, native, http.MethodGet, api+"/api/v1/me", nil, asBob)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, int64(22), countCategories(t, mongoURI, r.str("id")))
	bobDining := byKey(lists[0])["dining"]
	assert.NotContains(t, []string{groceries.ID, housing.ID, gymID}, bobDining.ID, "Bob has his own set")

	// DELETE /me erases Ana's categories and leaves Bob's.
	r = call(t, native, http.MethodGet, api+"/api/v1/me", nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	anaID := r.str("id")
	require.Equal(t, http.StatusNoContent, call(t, native, http.MethodDelete, api+"/api/v1/me", map[string]string{"password": password}, asAna).status)
	assert.Zero(t, countCategories(t, mongoURI, anaID), "DELETE /me erases the categories")
	assert.Len(t, mustList(t, native, categories, asBob), 22)
}

func TestCategoriesLimit(t *testing.T) {
	// 178 creates in a row would hit the per-user budget (100/m) first.
	t.Setenv("RATE_LIMIT_API_USER", "1000/m")
	t.Setenv("RATE_LIMIT_API_IP", "1000/m")
	api, mail, _, _ := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const addr = "many.categories@example.com"
	password := strings.Repeat("correct horse ", 2)
	signUp(t, api, mail, addr, password)
	auth := signIn(t, native, api, addr, password)

	body := map[string]any{"name": "Extra", "kind": "expense", "icon": "tag", "color": "#123456"}
	for i := range 200 - 22 {
		r := call(t, native, http.MethodPost, api+"/api/v1/categories", body, auth)
		require.Equal(t, http.StatusCreated, r.status, i)
	}
	r := call(t, native, http.MethodPost, api+"/api/v1/categories", body, auth)
	assert.Equal(t, http.StatusConflict, r.status, "the 22 defaults count toward the 200")
	assert.Equal(t, "limit_reached", r.str("code"))
	assert.Len(t, mustList(t, native, api+"/api/v1/categories", auth), 200)
}
