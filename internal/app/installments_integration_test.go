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

// installment is the part of an installment the tests compare.
type installment struct {
	id, date, invoice, description string
	amount                         float64
}

// installmentsOf lists the installments of group, by number.
func installmentsOf(t *testing.T, client *http.Client, txs, group string, auth map[string]string) map[int]installment {
	t.Helper()
	r := call(t, client, http.MethodGet, txs+"?installmentGroupId="+group, nil, auth)
	require.Equal(t, http.StatusOK, r.status, r.body)
	list, _ := r.body["transactions"].([]any)
	out := map[int]installment{}
	for _, raw := range list {
		tx, _ := raw.(map[string]any)
		inst, _ := tx["installment"].(map[string]any)
		require.Equal(t, group, inst["groupId"])
		invoice, _ := tx["invoiceMonth"].(string)
		out[int(inst["number"].(float64))] = installment{
			id: tx["id"].(string), date: tx["date"].(string), invoice: invoice,
			description: tx["description"].(string), amount: tx["amount"].(float64),
		}
	}
	return out
}

func TestInstallmentsEndToEnd(t *testing.T) {
	t.Setenv("RATE_LIMIT_API_USER", "1000/m")
	api, mail, mongoURI, _ := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const ana, bob = "ana.installments@example.com", "bob.installments@example.com"
	password := strings.Repeat("correct horse ", 2)
	signUp(t, api, mail, ana, password)
	signUp(t, api, mail, bob, password)
	asAna := signIn(t, native, api, ana, password)
	asBob := signIn(t, native, api, bob, password)
	accounts, txs := api+"/api/v1/accounts", api+"/api/v1/transactions"

	create := func(url string, body map[string]any) response {
		t.Helper()
		r := call(t, native, http.MethodPost, url, body, asAna)
		require.Equal(t, http.StatusCreated, r.status, r.body)
		return r
	}
	patch := func(id, scope string, body map[string]any) response {
		t.Helper()
		return call(t, native, http.MethodPatch, txs+"/"+id+"?scope="+scope, body, asAna)
	}
	card := create(accounts, map[string]any{
		"name": "Card", "type": "credit_card", "currency": "BRL", "card": map[string]any{"closingDay": 25, "dueDay": 5},
	}).str("id")
	bank := create(accounts, map[string]any{"name": "Bank", "type": "checking", "currency": "BRL"}).str("id")
	groceries := byKey(mustList(t, native, api+"/api/v1/categories", asAna))["groceries"].ID

	// Three installments on the card, the day before it closes: one per
	// invoice, the remainder on the first.
	first := create(txs, map[string]any{
		"kind": "expense", "accountId": card, "categoryId": groceries, "amount": 30001, "date": "2026-11-24",
		"description": "Couch", "installments": 3,
	})
	assert.Equal(t, "/api/v1/transactions/"+first.str("id"), first.header.Get("Location"), "the first installment")
	assert.Equal(t, float64(10001), first.body["amount"])
	inst, _ := first.body["installment"].(map[string]any)
	group, _ := inst["groupId"].(string)
	require.NotEmpty(t, group)
	assert.Equal(t, map[string]any{"groupId": group, "number": float64(1), "count": float64(3)}, inst)

	got := installmentsOf(t, native, txs, group, asAna)
	assert.Equal(t, map[int]installment{
		1: {id: first.str("id"), date: "2026-11-24", invoice: "2026-12", description: "Couch", amount: 10001},
		2: {id: got[2].id, date: "2026-12-24", invoice: "2027-01", description: "Couch", amount: 10000},
		3: {id: got[3].id, date: "2027-01-24", invoice: "2027-02", description: "Couch", amount: 10000},
	}, got)
	assert.Equal(t, []invoice{
		{month: "2026-12", status: "closed", total: 10001, remain: 10001},
		{month: "2027-01", status: "open", previous: 10001, total: 20001, remain: 20001},
		{month: "2027-02", status: "upcoming", previous: 20001, total: 30001, remain: 30001},
	}, invoicesOf(t, native, accounts+"/"+card+"/invoices", "from=2026-12&to=2027-02&asOf=2026-11-30", asAna),
		"the next invoices already hold their installments")

	// The second and the next move past the closing day: one invoice later.
	r := patch(got[2].id, "future", map[string]any{"date": "2026-12-26"})
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Equal(t, got[2].id, r.str("id"), "answers the one addressed")
	assert.Equal(t, "2027-02", r.str("invoiceMonth"))
	moved := installmentsOf(t, native, txs, group, asAna)
	assert.Equal(t, "2026-11-24", moved[1].date, "the earlier one stays")
	assert.Equal(t, "2026-12", moved[1].invoice)
	assert.Equal(t, "2027-01-26", moved[3].date)
	assert.Equal(t, "2027-03", moved[3].invoice)

	r = patch(got[2].id, "future", map[string]any{"amount": 12000})
	require.Equal(t, http.StatusOK, r.status, r.body)
	r = patch(got[3].id, "all", map[string]any{"description": "Sofa"})
	require.Equal(t, http.StatusOK, r.status, r.body)
	edited := installmentsOf(t, native, txs, group, asAna)
	assert.Equal(t, []float64{10001, 12000, 12000}, []float64{edited[1].amount, edited[2].amount, edited[3].amount})
	assert.Equal(t, []string{"Sofa", "Sofa", "Sofa"}, []string{edited[1].description, edited[2].description, edited[3].description})

	for name, scope := range map[string]string{"one installment": "this", "the next ones": "future", "no such scope": "everything"} {
		r := patch(got[1].id, scope, map[string]any{"accountId": bank})
		require.Equal(t, http.StatusUnprocessableEntity, r.status, name)
		assert.Equal(t, map[string]string{"scope": "invalid"}, fieldErrors(r), name)
	}

	// Another user reaches none of it.
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		r := call(t, native, method, txs+"/"+got[1].id+"?scope=all", map[string]any{"description": "x"}, asBob)
		assert.Equal(t, http.StatusNotFound, r.status, method)
	}
	ids, _ := transactionIDs(t, call(t, native, http.MethodGet, txs+"?installmentGroupId="+group, nil, asBob))
	assert.Empty(t, ids)

	// Deleting the second and the next leaves the first, numbered as it was.
	r = call(t, native, http.MethodDelete, txs+"/"+got[2].id+"?scope=future", nil, asAna)
	require.Equal(t, http.StatusNoContent, r.status, r.body)
	left := installmentsOf(t, native, txs, group, asAna)
	require.Len(t, left, 1)
	assert.Equal(t, got[1].id, left[1].id)

	// On a checking account (a payment book): monthly, with no invoice.
	book := create(txs, map[string]any{
		"kind": "expense", "accountId": bank, "categoryId": groceries, "amount": 5000, "date": "2027-01-31", "installments": 2,
	})
	assert.Nil(t, book.body["invoiceMonth"])
	bookGroup := book.body["installment"].(map[string]any)["groupId"].(string)
	assert.Equal(t, "2027-02-28", installmentsOf(t, native, txs, bookGroup, asAna)[2].date)

	for name, tc := range map[string]struct {
		body map[string]any
		want map[string]string
	}{
		"income": {
			map[string]any{"kind": "income", "accountId": bank, "categoryId": byKey(mustList(t, native, api+"/api/v1/categories", asAna))["salary"].ID,
				"amount": 1000, "date": "2026-11-08", "installments": 2},
			map[string]string{"installments": "invalid"},
		},
		"too many": {
			map[string]any{"kind": "expense", "accountId": bank, "categoryId": groceries, "amount": 1000, "date": "2026-11-08", "installments": 49},
			map[string]string{"installments": "out_of_range"},
		},
	} {
		r := call(t, native, http.MethodPost, txs, tc.body, asAna)
		require.Equal(t, http.StatusUnprocessableEntity, r.status, name)
		assert.Equal(t, tc.want, fieldErrors(r), name)
	}

	// DELETE /me erases the installments with the rest.
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
	assert.Zero(t, n)
}
