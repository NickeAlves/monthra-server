//go:build integration

package app

import (
	"context"
	"net/http"
	"net/url"
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

// transactionIDs returns the ids of a GET /transactions page, in order, and
// its next cursor ("" on the last page).
func transactionIDs(t *testing.T, r response) ([]string, string) {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status, r.body)
	list, _ := r.body["transactions"].([]any)
	out := make([]string, 0, len(list))
	for _, raw := range list {
		tx, _ := raw.(map[string]any)
		out = append(out, tx["id"].(string))
	}
	next, _ := r.body["nextCursor"].(string)
	return out, next
}

// fieldErrors returns the field codes of a problem answer.
func fieldErrors(r response) map[string]string {
	out := map[string]string{}
	list, _ := r.body["errors"].([]any)
	for _, raw := range list {
		f, _ := raw.(map[string]any)
		out[f["field"].(string)], _ = f["code"].(string)
	}
	return out
}

func TestTransactionsEndToEnd(t *testing.T) {
	t.Setenv("RATE_LIMIT_API_USER", "1000/m")
	api, mail, mongoURI, metricsURL := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const ana, bob = "ana.ledger@example.com", "bob.ledger@example.com"
	password := strings.Repeat("correct horse ", 2)
	signUp(t, api, mail, ana, password)
	signUp(t, api, mail, bob, password)
	asAna := signIn(t, native, api, ana, password)
	asBob := signIn(t, native, api, bob, password)
	accounts, txs := api+"/api/v1/accounts", api+"/api/v1/transactions"

	create := func(auth map[string]string, url string, body map[string]any) string {
		t.Helper()
		r := call(t, native, http.MethodPost, url, body, auth)
		require.Equal(t, http.StatusCreated, r.status, r.body)
		return r.str("id")
	}
	bank := create(asAna, accounts, map[string]any{"name": "Bank", "type": "checking", "currency": "BRL", "initialBalance": 100000})
	wallet := create(asAna, accounts, map[string]any{"name": "Wallet", "type": "cash", "currency": "BRL"})
	dollars := create(asAna, accounts, map[string]any{"name": "Dollars", "type": "savings", "currency": "USD"})
	bobsBank := create(asBob, accounts, map[string]any{"name": "Bob's", "type": "checking", "currency": "BRL"})
	cats := byKey(mustList(t, native, api+"/api/v1/categories", asAna))
	groceries, dining, salary := cats["groceries"].ID, cats["dining"].ID, cats["salary"].ID

	// Create the three kinds.
	r := call(t, native, http.MethodPost, txs, map[string]any{
		"kind": "expense", "accountId": bank, "categoryId": groceries, "amount": 4590, "date": "2026-11-01",
		"description": "  Café da manhã ", "tags": []string{"Breakfast", "breakfast"},
	}, asAna)
	require.Equal(t, http.StatusCreated, r.status, r.body)
	coffee := r.str("id")
	assert.Equal(t, "/api/v1/transactions/"+coffee, r.header.Get("Location"))
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	assert.Equal(t, "BRL", r.str("currency"))
	assert.Equal(t, "2026-11-01", r.str("date"))
	assert.Equal(t, "Café da manhã", r.str("description"))
	assert.Equal(t, []any{"breakfast"}, r.body["tags"])
	assert.Nil(t, r.body["toAccountId"])
	assert.NotContains(t, r.body, "descriptionSearch")
	assert.NotContains(t, r.body, "userId")

	pay := create(asAna, txs, map[string]any{"kind": "income", "accountId": bank, "categoryId": salary, "amount": 500000, "date": "2026-11-05"})
	r = call(t, native, http.MethodPost, txs, map[string]any{
		"kind": "transfer", "accountId": bank, "toAccountId": dollars, "amount": 100000, "toAmount": 19000, "date": "2026-11-06",
	}, asAna)
	require.Equal(t, http.StatusCreated, r.status, r.body)
	exchange := r.str("id")
	assert.Equal(t, "USD", r.str("toCurrency"))
	assert.Equal(t, float64(19000), r.body["toAmount"])
	assert.Nil(t, r.body["categoryId"])
	ahead := create(asAna, txs, map[string]any{"kind": "expense", "accountId": bank, "categoryId": groceries, "amount": 30000, "date": "2099-01-01"})

	for name, tc := range map[string]struct {
		body map[string]any
		want map[string]string
	}{
		"two currencies, no toAmount": {
			map[string]any{"kind": "transfer", "accountId": bank, "toAccountId": dollars, "amount": 1, "date": "2026-11-08"},
			map[string]string{"toAmount": "required"},
		},
		"income category on an expense": {
			map[string]any{"kind": "expense", "accountId": bank, "categoryId": salary, "amount": 1, "date": "2026-11-08"},
			map[string]string{"categoryId": "kind_mismatch"},
		},
		"another user's account": {
			map[string]any{"kind": "expense", "accountId": bobsBank, "categoryId": groceries, "amount": 1, "date": "2026-11-08"},
			map[string]string{"accountId": "invalid"},
		},
		"a bad day": {
			map[string]any{"kind": "income", "accountId": bank, "categoryId": salary, "amount": 1, "date": "2026-02-30"},
			map[string]string{"date": "invalid"},
		},
	} {
		r = call(t, native, http.MethodPost, txs, tc.body, asAna)
		require.Equal(t, http.StatusUnprocessableEntity, r.status, name)
		assert.Equal(t, tc.want, fieldErrors(r), name)
	}

	// Balances: up to the reader's day, and everything.
	r = call(t, native, http.MethodGet, accounts+"?asOf=2026-11-30", nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	balances := map[string][2]float64{}
	for _, raw := range r.body["accounts"].([]any) {
		a := raw.(map[string]any)
		balances[a["id"].(string)] = [2]float64{a["balance"].(float64), a["projectedBalance"].(float64)}
	}
	assert.Equal(t, map[string][2]float64{
		bank:    {100000 - 4590 + 500000 - 100000, 100000 - 4590 + 500000 - 100000 - 30000},
		wallet:  {0, 0},
		dollars: {19000, 19000},
	}, balances)
	r = call(t, native, http.MethodGet, accounts+"/"+bank+"?asOf=2026-11-03", nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, float64(100000-4590), r.body["balance"])
	r = call(t, native, http.MethodGet, accounts+"?asOf=soon", nil, asAna)
	assert.Equal(t, http.StatusUnprocessableEntity, r.status)

	// Pages of two, newest first, until the last one.
	var walked []string
	page := txs + "?limit=2"
	for range 5 {
		ids, next := transactionIDs(t, call(t, native, http.MethodGet, page, nil, asAna))
		walked = append(walked, ids...)
		if next == "" {
			break
		}
		page = txs + "?limit=2&cursor=" + url.QueryEscape(next)
	}
	assert.Equal(t, []string{ahead, exchange, pay, coffee}, walked)

	for query, want := range map[string][]string{
		"accountId=" + dollars:              {exchange},
		"accountId=" + bobsBank:             {},
		"q=cafe":                            {coffee},
		"tag=BREAKFAST":                     {coffee},
		"kind=income":                       {pay},
		"categoryId=" + groceries:           {ahead, coffee},
		"from=2026-11-05&to=2026-11-06":     {exchange, pay},
		"from=2026-11-01&accountId=" + bank: {ahead, exchange, pay, coffee},
	} {
		ids, next := transactionIDs(t, call(t, native, http.MethodGet, txs+"?"+query, nil, asAna))
		assert.Equal(t, want, ids, query)
		assert.Empty(t, next, query)
	}
	r = call(t, native, http.MethodGet, txs+"?from=2026-11-10&to=2026-11-01&limit=500", nil, asAna)
	require.Equal(t, http.StatusUnprocessableEntity, r.status)
	assert.Equal(t, map[string]string{"to": "invalid"}, fieldErrors(r))
	r = call(t, native, http.MethodGet, txs+"?limit=500", nil, asAna)
	assert.Equal(t, map[string]string{"limit": "out_of_range"}, fieldErrors(r))

	// Patch.
	r = call(t, native, http.MethodPatch, txs+"/"+coffee, map[string]any{"amount": 5000, "tags": []string{}}, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Equal(t, float64(5000), r.body["amount"])
	assert.Equal(t, []any{}, r.body["tags"])
	assert.Equal(t, "Café da manhã", r.str("description"), "untouched")
	r = call(t, native, http.MethodPatch, txs+"/"+coffee, map[string]any{"kind": "income"}, asAna)
	assert.Equal(t, http.StatusBadRequest, r.status, "the kind never changes")

	// Another user never reaches Ana's transactions.
	for _, tc := range []struct {
		method string
		body   any
	}{{http.MethodGet, nil}, {http.MethodPatch, map[string]any{"amount": 1}}, {http.MethodDelete, nil}} {
		r = call(t, native, tc.method, txs+"/"+coffee, tc.body, asBob)
		assert.Equal(t, http.StatusNotFound, r.status, tc.method)
		assert.Equal(t, "not_found", r.str("code"), tc.method)
	}
	ids, _ := transactionIDs(t, call(t, native, http.MethodGet, txs+"?accountId="+bank, nil, asBob))
	assert.Empty(t, ids)
	assert.Equal(t, http.StatusNotFound, call(t, native, http.MethodGet, txs+"/not-an-id", nil, asAna).status)

	// An account with transactions is archived, not deleted; an archived
	// account takes no new transactions.
	r = call(t, native, http.MethodDelete, accounts+"/"+dollars, nil, asAna)
	require.Equal(t, http.StatusConflict, r.status, "the destination of a transfer counts")
	assert.Equal(t, "account_in_use", r.str("code"))
	r = call(t, native, http.MethodPatch, accounts+"/"+dollars, map[string]any{"archived": true}, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, float64(19000), r.body["balance"], "the history stays")
	r = call(t, native, http.MethodPost, txs, map[string]any{
		"kind": "transfer", "accountId": bank, "toAccountId": dollars, "amount": 1, "toAmount": 1, "date": "2026-11-08",
	}, asAna)
	require.Equal(t, http.StatusUnprocessableEntity, r.status)
	assert.Equal(t, map[string]string{"toAccountId": "archived"}, fieldErrors(r))
	assert.Equal(t, http.StatusNoContent, call(t, native, http.MethodDelete, accounts+"/"+wallet, nil, asAna).status, "no transactions")

	r = call(t, native, http.MethodPatch, api+"/api/v1/me/preferences", map[string]any{"locale": "en-US"}, asAna)
	require.Equal(t, http.StatusOK, r.status)
	r = call(t, native, http.MethodDelete, accounts+"/"+bank, nil, asAna)
	require.Equal(t, http.StatusConflict, r.status)
	assert.Equal(t, "Account in use", r.str("title"))

	// A category in use moves its transactions to another of its kind.
	r = call(t, native, http.MethodDelete, api+"/api/v1/categories/"+groceries, nil, asAna)
	require.Equal(t, http.StatusConflict, r.status)
	assert.Equal(t, "category_in_use", r.str("code"))
	r = call(t, native, http.MethodDelete, api+"/api/v1/categories/"+groceries+"?reassignTo="+salary, nil, asAna)
	require.Equal(t, http.StatusUnprocessableEntity, r.status)
	assert.Equal(t, map[string]string{"reassignTo": "kind_mismatch"}, fieldErrors(r))
	r = call(t, native, http.MethodDelete, api+"/api/v1/categories/"+groceries+"?reassignTo="+dining, nil, asAna)
	require.Equal(t, http.StatusNoContent, r.status)
	for _, id := range []string{coffee, ahead} {
		r = call(t, native, http.MethodGet, txs+"/"+id, nil, asAna)
		require.Equal(t, http.StatusOK, r.status)
		assert.Equal(t, dining, r.str("categoryId"))
	}

	assert.Equal(t, http.StatusNoContent, call(t, native, http.MethodDelete, txs+"/"+ahead, nil, asAna).status)
	assert.Equal(t, http.StatusNotFound, call(t, native, http.MethodDelete, txs+"/"+ahead, nil, asAna).status)

	_, metrics := get(t, metricsURL)
	for _, line := range []string{
		`monthra_transactions_created_total{kind="expense"} 2`,
		`monthra_transactions_created_total{kind="income"} 1`,
		`monthra_transactions_created_total{kind="transfer"} 1`,
	} {
		assert.Contains(t, metrics, line)
	}

	// DELETE /me erases the transactions.
	r = call(t, native, http.MethodGet, api+"/api/v1/me", nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	anaID := r.str("id")
	r = call(t, native, http.MethodDelete, api+"/api/v1/me", map[string]string{"password": password}, asAna)
	require.Equal(t, http.StatusNoContent, r.status)
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	oid, err := bson.ObjectIDFromHex(anaID)
	require.NoError(t, err)
	n, err := client.Database("monthra").Collection(repomongo.CollTransactions).CountDocuments(ctx, bson.D{{Key: "user_id", Value: oid}})
	require.NoError(t, err)
	assert.Zero(t, n, "DELETE /me erases the transactions")
}
