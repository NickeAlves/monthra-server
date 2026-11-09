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

// positionOf returns the position of an investment answer.
func positionOf(t *testing.T, r response) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status, r.body)
	p, ok := r.body["position"].(map[string]any)
	require.True(t, ok, r.body)
	return p
}

func TestInvestmentsEndToEnd(t *testing.T) {
	t.Setenv("RATE_LIMIT_API_USER", "1000/m")
	api, mail, mongoURI, metricsURL := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const ana, bob = "ana.investments@example.com", "bob.investments@example.com"
	password := strings.Repeat("correct horse ", 2)
	signUp(t, api, mail, ana, password)
	signUp(t, api, mail, bob, password)
	asAna := signIn(t, native, api, ana, password)
	asBob := signIn(t, native, api, bob, password)
	investments := api + "/api/v1/investments"
	// Movements happen up to the reader's today: the days are relative.
	today := domain.DateOf(time.Now().UTC())
	ago := func(days int) string { return today.AddDays(-days).String() }

	create := func(url string, body map[string]any) response {
		t.Helper()
		r := call(t, native, http.MethodPost, url, body, asAna)
		require.Equal(t, http.StatusCreated, r.status, r.body)
		return r
	}
	broker := create(api+"/api/v1/accounts", map[string]any{"name": "Broker", "type": "brokerage", "currency": "BRL"}).str("id")

	r := create(investments, map[string]any{"name": "Monthra SA", "type": "stock", "currency": "BRL", "ticker": "mnth3", "accountId": broker})
	stock := r.str("id")
	assert.Equal(t, "/api/v1/investments/"+stock, r.header.Get("Location"))
	assert.Equal(t, "MNTH3", r.str("ticker"))
	assert.Equal(t, "units", r.str("mode"), "a stock trades in units")
	assert.Equal(t, broker, r.str("accountId"))
	p, _ := r.body["position"].(map[string]any)
	assert.Equal(t, "0", p["quantity"])
	assert.Nil(t, p["returnBps"], "nothing contributed yet")

	movements := investments + "/" + stock + "/movements"
	buy := create(movements, map[string]any{"type": "buy", "date": ago(20), "quantity": "10", "amount": 100000, "fees": 500}).str("id")
	// The sale comes before the second buy, so it relies on the first.
	r = create(movements, map[string]any{"type": "sell", "date": ago(15), "quantity": "5", "amount": 70000, "fees": 300})
	assert.Equal(t, "5", r.str("quantity"))
	assert.Nil(t, r.body["categoryId"])
	create(movements, map[string]any{"type": "buy", "date": ago(10), "quantity": "10", "amount": 120000, "fees": 500})
	r = create(movements+"?asOf="+today.String(), map[string]any{"type": "valuation", "date": ago(1), "price": "150"})
	assert.Equal(t, "/api/v1/investments/"+stock+"/movements/"+r.str("id"), r.header.Get("Location"))
	assert.Nil(t, r.body["amount"], "a price, no amount")

	p = positionOf(t, call(t, native, http.MethodGet, investments+"/"+stock, nil, asAna))
	assert.Equal(t, "15", p["quantity"])
	assert.Equal(t, "113.83333333", p["averagePrice"], "fees in the cost")
	assert.Equal(t, "150", p["price"])
	assert.Equal(t, ago(1), p["valuedOn"])
	assert.Equal(t, float64(225000), p["value"])
	assert.Equal(t, float64(170750), p["invested"])
	assert.Equal(t, float64(221000), p["contributed"])
	assert.Equal(t, float64(70000-300-50250), p["realizedGain"])
	assert.Equal(t, float64(225000-170750), p["unrealizedGain"])
	assert.Equal(t, float64(73700), p["totalReturn"])
	assert.Equal(t, float64(3335), p["returnBps"])

	// The position never goes below zero units.
	r = call(t, native, http.MethodPost, movements, map[string]any{"type": "sell", "date": today.String(), "quantity": "15.00000001", "amount": 1}, asAna)
	require.Equal(t, http.StatusUnprocessableEntity, r.status, r.body)
	assert.Equal(t, "exceeds_position", fieldCode(t, r))
	r = call(t, native, http.MethodDelete, movements+"/"+buy, nil, asAna)
	require.Equal(t, http.StatusConflict, r.status, r.body)
	assert.Equal(t, "exceeds_position", r.str("code"))
	r = call(t, native, http.MethodPost, movements, map[string]any{"type": "buy", "date": today.AddDays(2).String(), "quantity": "1", "amount": 1}, asAna)
	assert.Equal(t, http.StatusUnprocessableEntity, r.status, "no movement in the future")
	r = call(t, native, http.MethodPost, movements, map[string]any{"type": "buy", "date": today.String(), "quantity": 1, "amount": 1}, asAna)
	assert.Equal(t, http.StatusUnprocessableEntity, r.status, "a quantity is a decimal string")

	// A patch moves the numbers; the type never changes.
	r = call(t, native, http.MethodPatch, movements+"/"+buy, map[string]any{"fees": 0}, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Equal(t, float64(0), r.body["fees"])
	r = call(t, native, http.MethodPatch, movements+"/"+buy, map[string]any{"type": "sell"}, asAna)
	assert.Equal(t, http.StatusBadRequest, r.status)

	// The history, newest first, in pages.
	r = call(t, native, http.MethodGet, movements+"?limit=3", nil, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	page, _ := r.body["movements"].([]any)
	require.Len(t, page, 3)
	first, _ := page[0].(map[string]any)
	assert.Equal(t, "valuation", first["type"])
	next := r.str("nextCursor")
	require.NotEmpty(t, next)
	r = call(t, native, http.MethodGet, movements+"?limit=3&cursor="+next, nil, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	page, _ = r.body["movements"].([]any)
	require.Len(t, page, 1)
	assert.Nil(t, r.body["nextCursor"])
	r = call(t, native, http.MethodGet, movements+"?type=sell", nil, asAna)
	page, _ = r.body["movements"].([]any)
	assert.Len(t, page, 1)

	// Fixed income by value: money in, the value read on a statement.
	r = create(investments, map[string]any{"name": "CDB 110% CDI", "type": "fixed_income", "currency": "BRL"})
	cdb := r.str("id")
	assert.Equal(t, "value", r.str("mode"))
	create(investments+"/"+cdb+"/movements", map[string]any{"type": "buy", "date": ago(30), "amount": 100000})
	create(investments+"/"+cdb+"/movements", map[string]any{"type": "valuation", "date": ago(1), "amount": 101500})
	p = positionOf(t, call(t, native, http.MethodGet, investments+"/"+cdb, nil, asAna))
	assert.Nil(t, p["quantity"])
	assert.Equal(t, float64(101500), p["value"])
	assert.Equal(t, float64(1500), p["unrealizedGain"])
	r = call(t, native, http.MethodPost, investments+"/"+cdb+"/movements", map[string]any{"type": "buy", "date": today.String(), "quantity": "1", "amount": 1}, asAna)
	assert.Equal(t, http.StatusUnprocessableEntity, r.status, "no units by value")

	// The portfolio, whole, with totals by currency.
	r = call(t, native, http.MethodGet, investments, nil, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	list, _ := r.body["investments"].([]any)
	assert.Len(t, list, 2)
	totals, _ := r.body["totals"].([]any)
	require.Len(t, totals, 1)
	brl, _ := totals[0].(map[string]any)
	assert.Equal(t, "BRL", brl["currency"])
	assert.Equal(t, float64(225000+101500), brl["value"])
	assert.Equal(t, []any{
		map[string]any{"type": "stock", "value": float64(225000)},
		map[string]any{"type": "fixed_income", "value": float64(101500)},
	}, brl["byType"])

	// The mode and the currency never change; the account must match.
	r = call(t, native, http.MethodPatch, investments+"/"+stock, map[string]any{"currency": "USD"}, asAna)
	assert.Equal(t, http.StatusBadRequest, r.status)
	dollars := create(api+"/api/v1/accounts", map[string]any{"name": "US broker", "type": "brokerage", "currency": "USD"}).str("id")
	r = call(t, native, http.MethodPatch, investments+"/"+cdb, map[string]any{"accountId": dollars}, asAna)
	require.Equal(t, http.StatusUnprocessableEntity, r.status, r.body)
	assert.Equal(t, "currency_mismatch", fieldCode(t, r))
	r = call(t, native, http.MethodPatch, investments+"/"+stock, map[string]any{"name": "Monthra ON", "ticker": ""}, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Nil(t, r.body["ticker"], "cleared")

	// Bob reaches none of it.
	r = call(t, native, http.MethodGet, investments, nil, asBob)
	require.Equal(t, http.StatusOK, r.status)
	list, _ = r.body["investments"].([]any)
	assert.Empty(t, list)
	for _, req := range []struct {
		method, url string
		body        any
	}{
		{http.MethodGet, investments + "/" + stock, nil},
		{http.MethodPatch, investments + "/" + stock, map[string]any{"name": "Mine"}},
		{http.MethodDelete, investments + "/" + stock, nil},
		{http.MethodGet, movements, nil},
		{http.MethodPost, movements, map[string]any{"type": "fee", "date": today.String(), "amount": 1}},
		{http.MethodGet, movements + "/" + buy, nil},
		{http.MethodDelete, movements + "/" + buy, nil},
	} {
		r = call(t, native, req.method, req.url, req.body, asBob)
		assert.Equal(t, http.StatusNotFound, r.status, req.method+" "+req.url)
	}
	theirs := call(t, native, http.MethodPost, investments, map[string]any{"name": "Bob's", "type": "crypto", "currency": "BRL"}, asBob)
	require.Equal(t, http.StatusCreated, theirs.status, theirs.body)
	r = call(t, native, http.MethodGet, investments+"/"+theirs.str("id")+"/movements/"+buy, nil, asBob)
	assert.Equal(t, http.StatusNotFound, r.status, "Ana's movement through Bob's investment")
	r = call(t, native, http.MethodPost, investments, map[string]any{"name": "X", "type": "stock", "currency": "BRL", "accountId": broker}, asBob)
	assert.Equal(t, http.StatusUnprocessableEntity, r.status, "another user's account")

	_, metrics := get(t, metricsURL)
	assert.Contains(t, metrics, `monthra_investment_movements_created_total{type="buy"} 3`)

	// Deleting an investment deletes its movements.
	require.Equal(t, http.StatusNoContent, call(t, native, http.MethodDelete, investments+"/"+cdb, nil, asAna).status)
	assert.Equal(t, http.StatusNotFound, call(t, native, http.MethodGet, investments+"/"+cdb+"/movements", nil, asAna).status)

	// DELETE /me leaves no investment and no movement behind.
	r = call(t, native, http.MethodGet, api+"/api/v1/me", nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	oid, err := bson.ObjectIDFromHex(r.str("id"))
	require.NoError(t, err)
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	db := client.Database("monthra")
	n, err := db.Collection(repomongo.CollInvestmentMovements).CountDocuments(ctx, bson.D{{Key: "investment_id", Value: mustOID(t, cdb)}})
	require.NoError(t, err)
	assert.Zero(t, n, "the deleted investment's movements are gone")
	r = call(t, native, http.MethodDelete, api+"/api/v1/me", map[string]string{"password": password}, asAna)
	require.Equal(t, http.StatusNoContent, r.status)
	for _, coll := range []string{repomongo.CollInvestments, repomongo.CollInvestmentMovements} {
		n, err := db.Collection(coll).CountDocuments(ctx, bson.D{{Key: "user_id", Value: oid}})
		require.NoError(t, err)
		assert.Zero(t, n, coll)
	}
	r = call(t, native, http.MethodGet, investments, nil, asBob)
	list, _ = r.body["investments"].([]any)
	assert.Len(t, list, 1, "Bob's stays")
}

// fieldCode returns the code of the only field error of a 422 answer.
func fieldCode(t *testing.T, r response) string {
	t.Helper()
	list, _ := r.body["errors"].([]any)
	require.Len(t, list, 1, r.body)
	e, _ := list[0].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func mustOID(t *testing.T, id string) bson.ObjectID {
	t.Helper()
	oid, err := bson.ObjectIDFromHex(id)
	require.NoError(t, err)
	return oid
}
