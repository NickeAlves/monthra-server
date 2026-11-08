//go:build integration

package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

// accountIDs returns the ids of a GET /accounts answer, in order.
func accountIDs(t *testing.T, r response) []string {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status)
	list, _ := r.body["accounts"].([]any)
	out := make([]string, 0, len(list))
	for _, raw := range list {
		a, _ := raw.(map[string]any)
		out = append(out, a["id"].(string))
	}
	return out
}

func TestAccountsEndToEnd(t *testing.T) {
	api, mail, mongoURI, _ := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const ana, bob = "ana.accounts@example.com", "bob.accounts@example.com"
	password := strings.Repeat("correct horse ", 2)
	signUp(t, api, mail, ana, password)
	signUp(t, api, mail, bob, password)
	asAna := signIn(t, native, api, ana, password)
	asBob := signIn(t, native, api, bob, password)
	accounts := api + "/api/v1/accounts"

	r := call(t, native, http.MethodGet, accounts, nil, nil)
	assert.Equal(t, http.StatusUnauthorized, r.status, "behind Authenticate")
	assert.Empty(t, accountIDs(t, call(t, native, http.MethodGet, accounts, nil, asAna)))

	// Create.
	r = call(t, native, http.MethodPost, accounts, map[string]any{"name": "Card", "type": "credit_card", "currency": "BRL"}, asAna)
	require.Equal(t, http.StatusUnprocessableEntity, r.status)
	assert.Contains(t, r.body["errors"], map[string]any{"field": "card", "code": "required"})

	r = call(t, native, http.MethodPost, accounts, map[string]any{
		"name": "Card", "type": "credit_card", "currency": "BRL", "initialBalance": -120050,
		"card": map[string]any{"closingDay": 3, "dueDay": 10, "limit": 500000},
	}, asAna)
	require.Equal(t, http.StatusCreated, r.status)
	cardID := r.str("id")
	assert.Equal(t, "/api/v1/accounts/"+cardID, r.header.Get("Location"))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	assert.Equal(t, float64(-120050), r.body["initialBalance"], "minor units")
	assert.Equal(t, "manual", r.str("source"))

	r = call(t, native, http.MethodPost, accounts, map[string]any{"name": "Wallet", "type": "cash", "currency": "USD"}, asAna)
	require.Equal(t, http.StatusCreated, r.status)
	walletID := r.str("id")
	assert.Equal(t, []string{cardID, walletID}, accountIDs(t, call(t, native, http.MethodGet, accounts, nil, asAna)))

	// Another user never reaches Ana's accounts.
	for _, tc := range []struct {
		method string
		body   any
	}{{http.MethodGet, nil}, {http.MethodPatch, map[string]any{"name": "Mine"}}, {http.MethodDelete, nil}} {
		r = call(t, native, tc.method, accounts+"/"+cardID, tc.body, asBob)
		assert.Equal(t, http.StatusNotFound, r.status, tc.method)
		assert.Equal(t, "not_found", r.str("code"), tc.method)
	}
	assert.Empty(t, accountIDs(t, call(t, native, http.MethodGet, accounts+"?includeArchived=true", nil, asBob)))
	r = call(t, native, http.MethodGet, accounts+"/"+cardID, nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, "Card", r.str("name"), "untouched by Bob")
	assert.Equal(t, http.StatusNotFound, call(t, native, http.MethodGet, accounts+"/not-an-id", nil, asAna).status)

	// Update and archive.
	r = call(t, native, http.MethodPatch, accounts+"/"+cardID, map[string]any{"currency": "USD"}, asAna)
	assert.Equal(t, http.StatusBadRequest, r.status, "currency never changes")
	r = call(t, native, http.MethodPatch, accounts+"/"+cardID, map[string]any{"name": "Travel card", "archived": true}, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, "Travel card", r.str("name"))
	assert.Equal(t, true, r.body["archived"])
	assert.Equal(t, []string{walletID}, accountIDs(t, call(t, native, http.MethodGet, accounts, nil, asAna)))
	assert.Equal(t, []string{cardID, walletID}, accountIDs(t, call(t, native, http.MethodGet, accounts+"?includeArchived=true", nil, asAna)))

	// Errors answer in the stored locale.
	r = call(t, native, http.MethodPatch, api+"/api/v1/me/preferences", map[string]any{"locale": "en-US"}, asAna)
	require.Equal(t, http.StatusOK, r.status)
	r = call(t, native, http.MethodGet, accounts+"/"+bson.NewObjectID().Hex(), nil, map[string]string{
		"Authorization": asAna["Authorization"], "Accept-Language": "pt-BR",
	})
	assert.Equal(t, http.StatusNotFound, r.status)
	assert.Equal(t, "Not found", r.str("title"))

	// Delete.
	assert.Equal(t, http.StatusNoContent, call(t, native, http.MethodDelete, accounts+"/"+walletID, nil, asAna).status)
	assert.Equal(t, http.StatusNotFound, call(t, native, http.MethodDelete, accounts+"/"+walletID, nil, asAna).status)

	// Bob's own account survives Ana's deletion; Ana's accounts do not.
	r = call(t, native, http.MethodPost, accounts, map[string]any{"name": "Bank", "type": "checking", "currency": "EUR"}, asBob)
	require.Equal(t, http.StatusCreated, r.status)
	r = call(t, native, http.MethodGet, api+"/api/v1/me", nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	anaID := r.str("id")
	r = call(t, native, http.MethodDelete, api+"/api/v1/me", map[string]string{"password": password}, asAna)
	require.Equal(t, http.StatusNoContent, r.status)

	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	coll := client.Database("monthra").Collection(repomongo.CollAccounts)
	oid, err := bson.ObjectIDFromHex(anaID)
	require.NoError(t, err)
	n, err := coll.CountDocuments(ctx, bson.D{{Key: "user_id", Value: oid}})
	require.NoError(t, err)
	assert.Zero(t, n, "DELETE /me erases the accounts")
	assert.Len(t, accountIDs(t, call(t, native, http.MethodGet, accounts, nil, asBob)), 1)
}

func TestAccountsLimit(t *testing.T) {
	// 101 creates in a row would hit the per-user budget (100/m) first.
	t.Setenv("RATE_LIMIT_API_USER", "1000/m")
	t.Setenv("RATE_LIMIT_API_IP", "1000/m")
	api, mail, _, _ := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const addr = "many.accounts@example.com"
	password := strings.Repeat("correct horse ", 2)
	signUp(t, api, mail, addr, password)
	auth := signIn(t, native, api, addr, password)

	body := map[string]any{"name": "Cash", "type": "cash", "currency": "BRL"}
	for i := range 100 {
		r := call(t, native, http.MethodPost, api+"/api/v1/accounts", body, auth)
		require.Equal(t, http.StatusCreated, r.status, i)
	}
	r := call(t, native, http.MethodPost, api+"/api/v1/accounts", body, auth)
	assert.Equal(t, http.StatusConflict, r.status)
	assert.Equal(t, "limit_reached", r.str("code"))
}
