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

// occurrence is the part of an occurrence the tests compare.
type occurrence struct {
	id, date, invoice string
	amount            float64
}

// occurrencesOf lists the occurrences of recurrence id, oldest first.
func occurrencesOf(t *testing.T, client *http.Client, txs, id string, auth map[string]string) []occurrence {
	t.Helper()
	r := call(t, client, http.MethodGet, txs+"?recurrenceId="+id, nil, auth)
	require.Equal(t, http.StatusOK, r.status, r.body)
	list, _ := r.body["transactions"].([]any)
	out := make([]occurrence, len(list))
	for i, raw := range list {
		tx, _ := raw.(map[string]any)
		link, _ := tx["recurrence"].(map[string]any)
		require.Equal(t, id, link["id"])
		require.Equal(t, tx["date"], link["occurrenceDate"])
		invoice, _ := tx["invoiceMonth"].(string)
		// Newest first in the listing: reverse it.
		out[len(list)-1-i] = occurrence{id: tx["id"].(string), date: tx["date"].(string), invoice: invoice, amount: tx["amount"].(float64)}
	}
	return out
}

func days(list []occurrence) []string {
	out := make([]string, len(list))
	for i, o := range list {
		out[i] = o.date
	}
	return out
}

func TestRecurrencesEndToEnd(t *testing.T) {
	t.Setenv("RATE_LIMIT_API_USER", "1000/m")
	api, mail, mongoURI, _ := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const ana, bob = "ana.recurrences@example.com", "bob.recurrences@example.com"
	password := strings.Repeat("correct horse ", 2)
	signUp(t, api, mail, ana, password)
	signUp(t, api, mail, bob, password)
	asAna := signIn(t, native, api, ana, password)
	asBob := signIn(t, native, api, bob, password)
	accounts, txs, recs := api+"/api/v1/accounts", api+"/api/v1/transactions", api+"/api/v1/recurrences"
	// The server creates occurrences up to today: the days are relative.
	today := domain.DateOf(time.Now().UTC())
	start := today.AddMonths(-3)

	create := func(url string, body map[string]any) response {
		t.Helper()
		r := call(t, native, http.MethodPost, url, body, asAna)
		require.Equal(t, http.StatusCreated, r.status, r.body)
		return r
	}
	bank := create(accounts, map[string]any{"name": "Bank", "type": "checking", "currency": "BRL", "initialBalance": 1000000}).str("id")
	savings := create(accounts, map[string]any{"name": "Savings", "type": "savings", "currency": "BRL"}).str("id")
	spare := create(accounts, map[string]any{"name": "Spare", "type": "checking", "currency": "BRL"}).str("id")
	card := create(accounts, map[string]any{
		"name": "Card", "type": "credit_card", "currency": "BRL", "card": map[string]any{"closingDay": 25, "dueDay": 5},
	}).str("id")
	categories := byKey(mustList(t, native, api+"/api/v1/categories", asAna))
	housing, subscriptions := categories["housing"].ID, categories["subscriptions"].ID

	// A rent since three months ago: its history is created right away.
	rent := create(recs, map[string]any{
		"kind": "expense", "accountId": bank, "categoryId": housing, "amount": 250000, "description": "Rent",
		"frequency": "monthly", "startDate": start.String(),
	})
	rentID := rent.str("id")
	assert.Equal(t, "/api/v1/recurrences/"+rentID, rent.header.Get("Location"))
	assert.Equal(t, start.AddMonths(3).String(), rent.str("lastDate"))
	assert.Equal(t, start.AddMonths(4).String(), rent.str("nextDate"))
	history := occurrencesOf(t, native, txs, rentID, asAna)
	assert.Equal(t, []string{start.String(), start.AddMonths(1).String(), start.AddMonths(2).String(), start.AddMonths(3).String()}, days(history))

	// Reading again creates nothing more, and the balance counts them.
	for range 2 {
		r := call(t, native, http.MethodGet, txs, nil, asAna)
		require.Equal(t, http.StatusOK, r.status)
	}
	assert.Len(t, occurrencesOf(t, native, txs, rentID, asAna), 4)
	r := call(t, native, http.MethodGet, accounts+"/"+bank, nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, float64(0), r.body["balance"], "1.000.000 less four rents")

	// A subscription on the card goes to the invoices.
	streaming := create(recs, map[string]any{
		"kind": "expense", "accountId": card, "categoryId": subscriptions, "amount": 3990, "description": "Streaming",
		"frequency": "monthly", "startDate": today.AddMonths(-1).String(),
	}).str("id")
	for _, o := range occurrencesOf(t, native, txs, streaming, asAna) {
		assert.NotEmpty(t, o.invoice, "on the card, in an invoice")
	}

	// A weekly saving, three times from today.
	saving := create(recs, map[string]any{
		"kind": "transfer", "accountId": bank, "toAccountId": savings, "amount": 50000, "frequency": "weekly",
		"startDate": today.String(), "count": 3,
	})
	assert.Equal(t, today.AddDays(14).String(), saving.str("endDate"))
	assert.Equal(t, today.AddDays(7).String(), saving.str("nextDate"))
	assert.Len(t, occurrencesOf(t, native, txs, saving.str("id"), asAna), 1)

	// The rent goes up from the third one on: the later occurrences and the
	// recurrence follow, the earlier ones stay.
	r = call(t, native, http.MethodPatch, txs+"/"+history[2].id+"?scope=future", map[string]any{"amount": 270000}, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	raised := occurrencesOf(t, native, txs, rentID, asAna)
	assert.Equal(t, []float64{250000, 250000, 270000, 270000}, []float64{raised[0].amount, raised[1].amount, raised[2].amount, raised[3].amount})
	r = call(t, native, http.MethodGet, recs+"/"+rentID, nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, float64(270000), r.body["amount"], "the next ones too")
	r = call(t, native, http.MethodPatch, txs+"/"+history[2].id+"?scope=future", map[string]any{"date": today.String()}, asAna)
	require.Equal(t, http.StatusUnprocessableEntity, r.status)
	assert.Equal(t, map[string]string{"scope": "invalid"}, fieldErrors(r), "the rule gives the days")

	// The projection: the next rent at its new amount.
	r = call(t, native, http.MethodGet, recs+"/upcoming?to="+today.AddDays(40).String(), nil, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	upcoming, _ := r.body["occurrences"].([]any)
	var nextRent map[string]any
	for _, raw := range upcoming {
		if o := raw.(map[string]any); o["recurrenceId"] == rentID {
			nextRent = o
			break
		}
	}
	require.NotNil(t, nextRent)
	assert.Equal(t, start.AddMonths(4).String(), nextRent["date"])
	assert.Equal(t, float64(270000), nextRent["amount"])
	assert.Len(t, occurrencesOf(t, native, txs, rentID, asAna), 4, "a projection stores nothing")

	// An active recurrence keeps its account from being archived, and any
	// from being deleted.
	r = call(t, native, http.MethodPatch, accounts+"/"+bank, map[string]any{"archived": true}, asAna)
	require.Equal(t, http.StatusConflict, r.status)
	assert.Equal(t, "used_by_recurrence", r.str("code"))
	create(recs, map[string]any{
		"kind": "expense", "accountId": spare, "categoryId": housing, "amount": 100, "frequency": "yearly",
		"startDate": today.AddDays(10).String(),
	})
	r = call(t, native, http.MethodDelete, accounts+"/"+spare, nil, asAna)
	require.Equal(t, http.StatusConflict, r.status)
	assert.Equal(t, "used_by_recurrence", r.str("code"))
	r = call(t, native, http.MethodDelete, api+"/api/v1/categories/"+housing, nil, asAna)
	require.Equal(t, http.StatusConflict, r.status)
	assert.Equal(t, "category_in_use", r.str("code"), "transactions first")

	// Another user reaches none of it.
	for _, tc := range []struct{ method, url string }{
		{http.MethodGet, recs + "/" + rentID},
		{http.MethodPatch, recs + "/" + rentID},
		{http.MethodDelete, recs + "/" + rentID},
		{http.MethodPatch, txs + "/" + history[0].id + "?scope=all"},
		{http.MethodDelete, txs + "/" + history[0].id + "?scope=all"},
	} {
		r := call(t, native, tc.method, tc.url, map[string]any{"amount": 1}, asBob)
		assert.Equal(t, http.StatusNotFound, r.status, tc.method+" "+tc.url)
	}
	ids, _ := transactionIDs(t, call(t, native, http.MethodGet, txs+"?recurrenceId="+rentID, nil, asBob))
	assert.Empty(t, ids)
	r = call(t, native, http.MethodGet, recs, nil, asBob)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, []any{}, r.body["recurrences"])

	// Deleting from the third rent on ends the rule before it.
	r = call(t, native, http.MethodDelete, txs+"/"+history[2].id+"?scope=future", nil, asAna)
	require.Equal(t, http.StatusNoContent, r.status, r.body)
	assert.Len(t, occurrencesOf(t, native, txs, rentID, asAna), 2)
	r = call(t, native, http.MethodGet, recs+"/"+rentID, nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Nil(t, r.body["nextDate"], "ended")
	assert.Equal(t, start.AddMonths(2).AddDays(-1).String(), r.str("endDate"))
	r = call(t, native, http.MethodPatch, accounts+"/"+bank, map[string]any{"archived": true}, asAna)
	assert.Equal(t, http.StatusConflict, r.status, "the saving still runs on it")

	// Deleting a recurrence keeps its transactions; deleting with scope all
	// takes both.
	r = call(t, native, http.MethodDelete, recs+"/"+streaming, nil, asAna)
	require.Equal(t, http.StatusNoContent, r.status)
	assert.NotEmpty(t, occurrencesOf(t, native, txs, streaming, asAna))
	first := occurrencesOf(t, native, txs, saving.str("id"), asAna)[0]
	r = call(t, native, http.MethodDelete, txs+"/"+first.id+"?scope=all", nil, asAna)
	require.Equal(t, http.StatusNoContent, r.status)
	assert.Empty(t, occurrencesOf(t, native, txs, saving.str("id"), asAna))
	r = call(t, native, http.MethodGet, recs+"/"+saving.str("id"), nil, asAna)
	assert.Equal(t, http.StatusNotFound, r.status)
	r = call(t, native, http.MethodPatch, accounts+"/"+bank, map[string]any{"archived": true}, asAna)
	assert.Equal(t, http.StatusOK, r.status, "no active recurrence on it any more")

	for name, tc := range map[string]struct {
		body map[string]any
		want map[string]string
	}{
		"frequency": {
			map[string]any{"kind": "expense", "accountId": spare, "categoryId": housing, "amount": 1, "frequency": "hourly", "startDate": today.String()},
			map[string]string{"frequency": "invalid"},
		},
		"archived account": {
			map[string]any{"kind": "expense", "accountId": bank, "categoryId": housing, "amount": 1, "frequency": "daily", "startDate": today.String()},
			map[string]string{"accountId": "archived"},
		},
	} {
		r := call(t, native, http.MethodPost, recs, tc.body, asAna)
		require.Equal(t, http.StatusUnprocessableEntity, r.status, name)
		assert.Equal(t, tc.want, fieldErrors(r), name)
	}

	// DELETE /me erases the recurrences and their transactions.
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
	for _, coll := range []string{repomongo.CollRecurrences, repomongo.CollTransactions} {
		n, err := client.Database("monthra").Collection(coll).CountDocuments(ctx, bson.D{{Key: "user_id", Value: oid}})
		require.NoError(t, err)
		assert.Zero(t, n, coll)
	}
}
