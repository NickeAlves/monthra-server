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

	"github.com/NickeAlves/monthra-server/internal/domain"
	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

// budgetsOf returns the budgets of a GET /budgets answer by category id.
func budgetsOf(t *testing.T, r response) map[string]map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status, r.body)
	out := map[string]map[string]any{}
	list, _ := r.body["budgets"].([]any)
	for _, raw := range list {
		b, _ := raw.(map[string]any)
		id, _ := b["categoryId"].(string)
		out[id] = b
	}
	return out
}

func TestBudgetsEndToEnd(t *testing.T) {
	t.Setenv("RATE_LIMIT_API_USER", "1000/m")
	api, mail, mongoURI, metricsURL := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const ana, bob = "ana.budgets@example.com", "bob.budgets@example.com"
	password := strings.Repeat("correct horse ", 2)
	signUp(t, api, mail, ana, password)
	signUp(t, api, mail, bob, password)
	asAna := signIn(t, native, api, ana, password)
	asBob := signIn(t, native, api, bob, password)
	budgets, txs := api+"/api/v1/budgets", api+"/api/v1/transactions"
	// The server counts the reader's month: the days are relative.
	today := domain.DateOf(time.Now().UTC())
	month := domain.MonthOf(today)

	create := func(url string, body map[string]any) response {
		t.Helper()
		r := call(t, native, http.MethodPost, url, body, asAna)
		require.Equal(t, http.StatusCreated, r.status, r.body)
		return r
	}
	bank := create(api+"/api/v1/accounts", map[string]any{"name": "Bank", "type": "checking", "currency": "BRL"}).str("id")
	spend := func(category string, amount int64) {
		t.Helper()
		create(txs, map[string]any{"kind": "expense", "accountId": bank, "categoryId": category, "amount": amount, "date": today.String()})
	}
	categories := byKey(mustList(t, native, api+"/api/v1/categories", asAna))
	groceries, dining, shopping, travel, salary := categories["groceries"].ID, categories["dining"].ID,
		categories["shopping"].ID, categories["travel"].ID, categories["salary"].ID

	r := create(budgets, map[string]any{"categoryId": groceries, "currency": "BRL", "limit": 100000})
	groceriesBudget := r.str("id")
	assert.Equal(t, "/api/v1/budgets/"+groceriesBudget, r.header.Get("Location"))
	assert.Equal(t, month.String(), r.str("month"), "the reader's month")
	assert.Equal(t, "ok", r.str("status"))
	assert.Equal(t, float64(80), r.body["warnAt"])

	r = call(t, native, http.MethodPost, budgets, map[string]any{"categoryId": groceries, "currency": "USD", "limit": 5}, asAna)
	assert.Equal(t, http.StatusConflict, r.status)
	assert.Equal(t, "budget_exists", r.str("code"))
	for name, body := range map[string]map[string]any{
		"income":     {"categoryId": salary, "currency": "BRL", "limit": 1},
		"next month": {"categoryId": dining, "currency": "BRL", "limit": 1, "month": month.AddMonths(1).String()},
		"no limit":   {"categoryId": dining, "currency": "BRL"},
	} {
		r = call(t, native, http.MethodPost, budgets, body, asAna)
		assert.Equal(t, http.StatusUnprocessableEntity, r.status, name)
	}

	// 70% gives no alert, 85% the warning, past 100% the exceeded one,
	// and after that nothing more.
	sent := mail.Count(t, ana)
	spend(groceries, 70000)
	spend(groceries, 15000)
	warning := mail.WaitForEmail(t, ana, "está perto do limite")
	assert.Contains(t, warning.Text, webOrigin+"/budgets?month="+month.String())
	assert.NotContains(t, warning.Text, "Mercado", "no category name in an email")
	r = call(t, native, http.MethodGet, budgets, nil, asAna)
	b := budgetsOf(t, r)[groceries]
	assert.Equal(t, "warning", b["status"])
	assert.Equal(t, float64(85000), b["spent"])
	assert.Equal(t, float64(15000), b["remaining"])
	spend(groceries, 20000)
	mail.WaitForEmail(t, ana, "passou do limite")
	spend(groceries, 1000)
	time.Sleep(500 * time.Millisecond) // the mailer is asynchronous
	assert.Equal(t, sent+2, mail.Count(t, ana), "each alert once")
	r = call(t, native, http.MethodGet, budgets+"/"+groceriesBudget, nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, "exceeded", r.str("status"))
	assert.Equal(t, float64(-6000), r.body["remaining"])

	// Raising the limit rearms the alerts; muting them keeps the status.
	r = call(t, native, http.MethodPatch, budgets+"/"+groceriesBudget, map[string]any{"limit": 10000000}, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Equal(t, "ok", r.str("status"))
	r = call(t, native, http.MethodPatch, api+"/api/v1/me/preferences", map[string]any{"budgetAlerts": false}, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, false, r.body["budgetAlerts"])
	create(budgets, map[string]any{"categoryId": shopping, "currency": "BRL", "limit": 1000})
	spend(shopping, 5000)
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, sent+2, mail.Count(t, ana), "muted")
	r = call(t, native, http.MethodGet, budgets, nil, asAna)
	assert.Equal(t, "exceeded", budgetsOf(t, r)[shopping]["status"])
	_, metrics := get(t, metricsURL)
	assert.Contains(t, metrics, `monthra_budget_alerts_total{level="warning",result="sent"} 1`)
	assert.Contains(t, metrics, `monthra_budget_alerts_total{level="exceeded",result="sent"} 1`)
	assert.Contains(t, metrics, `monthra_budget_alerts_total{level="exceeded",result="muted"} 1`)

	// A month gone by keeps its own budgets; the next one is a projection
	// of this one.
	prev := month.AddMonths(-1)
	r = create(budgets, map[string]any{"categoryId": travel, "currency": "EUR", "limit": 30000, "month": prev.String()})
	assert.Equal(t, prev.String(), r.str("month"))
	r = call(t, native, http.MethodGet, budgets+"?month="+prev.String(), nil, asAna)
	assert.Len(t, budgetsOf(t, r), 1)
	r = call(t, native, http.MethodGet, budgets+"?month="+month.AddMonths(1).String(), nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, true, r.body["projected"])
	next := budgetsOf(t, r)
	assert.Len(t, next, 2)
	assert.Nil(t, next[groceries]["id"])

	// The month starting: with the rollover mark set back to last month,
	// the next read carries last month's budgets over.
	r = call(t, native, http.MethodGet, api+"/api/v1/me", nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	anaID := r.str("id")
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	db := client.Database("monthra")
	oid, err := bson.ObjectIDFromHex(anaID)
	require.NoError(t, err)
	_, err = db.Collection(repomongo.CollUsers).UpdateOne(ctx, bson.D{{Key: "_id", Value: oid}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "budgets_rolled_through", Value: prev.String()}}}})
	require.NoError(t, err)
	r = call(t, native, http.MethodGet, budgets, nil, asAna)
	current := budgetsOf(t, r)
	require.Len(t, current, 3, "travel joined this month")
	assert.Equal(t, "EUR", current[travel]["currency"])
	assert.Equal(t, month.String(), current[travel]["month"])
	assert.Equal(t, float64(10000000), current[groceries]["limit"], "this month's own budget stays")

	// Deleting it this month leaves it out of the next one.
	travelID, _ := current[travel]["id"].(string)
	require.Equal(t, http.StatusNoContent, call(t, native, http.MethodDelete, budgets+"/"+travelID, nil, asAna).status)
	r = call(t, native, http.MethodGet, budgets+"?month="+month.AddMonths(1).String(), nil, asAna)
	assert.NotContains(t, budgetsOf(t, r), travel)

	// Bob reaches none of it.
	r = call(t, native, http.MethodGet, budgets, nil, asBob)
	assert.Empty(t, budgetsOf(t, r))
	for _, req := range []struct {
		method string
		body   any
	}{{http.MethodGet, nil}, {http.MethodPatch, map[string]any{"limit": 1}}, {http.MethodDelete, nil}} {
		r = call(t, native, req.method, budgets+"/"+groceriesBudget, req.body, asBob)
		assert.Equal(t, http.StatusNotFound, r.status, req.method)
	}
	r = call(t, native, http.MethodPost, budgets, map[string]any{"categoryId": groceries, "currency": "BRL", "limit": 1}, asBob)
	assert.Equal(t, http.StatusUnprocessableEntity, r.status, "another user's category")

	// Deleting a category drops its budgets in every month.
	r = call(t, native, http.MethodDelete, api+"/api/v1/categories/"+groceries+"?reassignTo="+dining, nil, asAna)
	require.Equal(t, http.StatusNoContent, r.status, r.body)
	assert.Equal(t, http.StatusNotFound, call(t, native, http.MethodGet, budgets+"/"+groceriesBudget, nil, asAna).status)
	r = call(t, native, http.MethodGet, budgets+"?month="+month.AddMonths(1).String(), nil, asAna)
	assert.NotContains(t, budgetsOf(t, r), groceries)

	// DELETE /me leaves no budget behind.
	r = call(t, native, http.MethodDelete, api+"/api/v1/me", map[string]string{"password": password}, asAna)
	require.Equal(t, http.StatusNoContent, r.status)
	n, err := db.Collection(repomongo.CollBudgets).CountDocuments(ctx, bson.D{{Key: "user_id", Value: oid}})
	require.NoError(t, err)
	assert.Zero(t, n)
}
