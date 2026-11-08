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

// invoice is the part of an invoice the tests compare.
type invoice struct {
	month, status                 string
	previous, total, paid, remain float64
}

// invoicesOf reads GET /accounts/{id}/invoices with query q.
func invoicesOf(t *testing.T, client *http.Client, url, q string, auth map[string]string) []invoice {
	t.Helper()
	r := call(t, client, http.MethodGet, url+"?"+q, nil, auth)
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	list, _ := r.body["invoices"].([]any)
	out := make([]invoice, len(list))
	for i, raw := range list {
		inv, _ := raw.(map[string]any)
		out[i] = invoice{
			month: inv["month"].(string), status: inv["status"].(string),
			previous: inv["previousBalance"].(float64), total: inv["total"].(float64),
			paid: inv["paid"].(float64), remain: inv["remaining"].(float64),
		}
	}
	return out
}

func TestCardInvoicesEndToEnd(t *testing.T) {
	t.Setenv("RATE_LIMIT_API_USER", "1000/m")
	api, mail, mongoURI, _ := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const ana, bob = "ana.card@example.com", "bob.card@example.com"
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
	// Closes on the 25th, falls due on the 5th, with 50.00 of opening debt.
	card := create(accounts, map[string]any{
		"name": "Card", "type": "credit_card", "currency": "BRL", "initialBalance": -5000,
		"card": map[string]any{"closingDay": 25, "dueDay": 5},
	}).str("id")
	bank := create(accounts, map[string]any{"name": "Bank", "type": "checking", "currency": "BRL", "initialBalance": 1000000}).str("id")
	cats := byKey(mustList(t, native, api+"/api/v1/categories", asAna))
	groceries, salary := cats["groceries"].ID, cats["salary"].ID
	invoices := accounts + "/" + card + "/invoices"

	// The eve of the closing day goes to December, the closing day to January.
	eve := create(txs, map[string]any{"kind": "expense", "accountId": card, "categoryId": groceries, "amount": 10000, "date": "2026-11-24"})
	assert.Equal(t, "2026-12", eve.str("invoiceMonth"))
	assert.Nil(t, eve.body["toInvoiceMonth"])
	closing := create(txs, map[string]any{"kind": "expense", "accountId": card, "categoryId": groceries, "amount": 20000, "date": "2026-11-25"})
	assert.Equal(t, "2027-01", closing.str("invoiceMonth"))
	refund := create(txs, map[string]any{"kind": "income", "accountId": card, "categoryId": salary, "amount": 1000, "date": "2026-11-26"})
	assert.Equal(t, "2027-01", refund.str("invoiceMonth"), "a credit belongs to an invoice")

	assert.Equal(t, []invoice{
		{month: "2026-12", status: "closed", previous: 5000, total: 15000, remain: 15000},
		{month: "2027-01", status: "open", previous: 15000, total: 34000, remain: 34000},
		{month: "2027-02", status: "upcoming", previous: 34000, total: 34000, remain: 34000},
	}, invoicesOf(t, native, invoices, "from=2026-12&to=2027-02&asOf=2026-11-30", asAna), "the opening debt rolls in")
	current := invoicesOf(t, native, invoices, "asOf=2026-11-30", asAna)
	require.Len(t, current, 1)
	assert.Equal(t, "2027-01", current[0].month, "without a range, the open invoice")

	// Pay part of December: the payment goes to the last invoice closed.
	partial := create(txs, map[string]any{"kind": "transfer", "accountId": bank, "toAccountId": card, "amount": 10000, "date": "2026-12-03"})
	assert.Equal(t, "2026-12", partial.str("toInvoiceMonth"))
	assert.Nil(t, partial.body["invoiceMonth"], "the bank has no invoice")
	assert.Equal(t, []invoice{
		{month: "2026-12", status: "overdue", previous: 5000, total: 15000, paid: 10000, remain: 5000},
		{month: "2027-01", status: "open", previous: 5000, total: 24000, remain: 24000},
	}, invoicesOf(t, native, invoices, "from=2026-12&to=2027-01&asOf=2026-12-10", asAna), "what is left rolls into January")
	assert.Equal(t, []invoice{
		{month: "2026-12", status: "carried_over", previous: 5000, total: 15000, paid: 10000, remain: 5000},
		{month: "2027-01", status: "closed", previous: 5000, total: 24000, remain: 24000},
	}, invoicesOf(t, native, invoices, "from=2026-12&to=2027-01&asOf=2026-12-26", asAna))

	// Paying January settles everything.
	rest := create(txs, map[string]any{"kind": "transfer", "accountId": bank, "toAccountId": card, "amount": 24000, "date": "2026-12-28"})
	assert.Equal(t, "2027-01", rest.str("toInvoiceMonth"))
	got := invoicesOf(t, native, invoices, "from=2027-01&to=2027-01&asOf=2027-01-02", asAna)
	assert.Equal(t, []invoice{{month: "2027-01", status: "paid", previous: 5000, total: 24000, paid: 24000}}, got)
	r := call(t, native, http.MethodGet, accounts+"/"+card+"?asOf=2027-01-02", nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, float64(0), r.body["balance"], "the card's balance agrees with its last invoice")

	// An invoice lists its charges, credits and payments.
	ids, next := transactionIDs(t, call(t, native, http.MethodGet, txs+"?accountId="+card+"&invoice=2026-12", nil, asAna))
	assert.Equal(t, []string{partial.str("id"), eve.str("id")}, ids)
	assert.Empty(t, next)
	ids, _ = transactionIDs(t, call(t, native, http.MethodGet, txs+"?accountId="+card+"&invoice=2027-01", nil, asAna))
	assert.Equal(t, []string{rest.str("id"), refund.str("id"), closing.str("id")}, ids)
	r = call(t, native, http.MethodGet, txs+"?invoice=2026-12", nil, asAna)
	require.Equal(t, http.StatusUnprocessableEntity, r.status)
	assert.Equal(t, map[string]string{"accountId": "required"}, fieldErrors(r))

	// The owner may name another invoice, within 12 months.
	late := create(txs, map[string]any{
		"kind": "expense", "accountId": card, "categoryId": groceries, "amount": 500, "date": "2026-11-24", "invoiceMonth": "2027-01",
	})
	assert.Equal(t, "2027-01", late.str("invoiceMonth"), "a purchase the bank posted late")
	for name, tc := range map[string]struct {
		body map[string]any
		want map[string]string
	}{
		"13 months away": {
			map[string]any{"kind": "expense", "accountId": card, "categoryId": groceries, "amount": 1, "date": "2026-11-24", "invoiceMonth": "2028-01"},
			map[string]string{"invoiceMonth": "out_of_range"},
		},
		"off a card": {
			map[string]any{"kind": "expense", "accountId": bank, "categoryId": groceries, "amount": 1, "date": "2026-11-24", "invoiceMonth": "2026-12"},
			map[string]string{"invoiceMonth": "invalid"},
		},
		"a payment on an expense": {
			map[string]any{"kind": "expense", "accountId": card, "categoryId": groceries, "amount": 1, "date": "2026-11-24", "toInvoiceMonth": "2026-12"},
			map[string]string{"toInvoiceMonth": "invalid"},
		},
	} {
		r := call(t, native, http.MethodPost, txs, tc.body, asAna)
		require.Equal(t, http.StatusUnprocessableEntity, r.status, name)
		assert.Equal(t, tc.want, fieldErrors(r), name)
	}

	// A new day picks the invoice again; naming one moves only the invoice.
	r = call(t, native, http.MethodPatch, txs+"/"+late.str("id"), map[string]any{"date": "2026-11-20"}, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Equal(t, "2026-12", r.str("invoiceMonth"))
	r = call(t, native, http.MethodPatch, txs+"/"+late.str("id"), map[string]any{"invoiceMonth": "2027-02"}, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Equal(t, "2027-02", r.str("invoiceMonth"))
	assert.Equal(t, "2026-11-20", r.str("date"))
	r = call(t, native, http.MethodPatch, txs+"/"+late.str("id"), map[string]any{"accountId": bank}, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Nil(t, r.body["invoiceMonth"], "off the card, no invoice")

	// Only cards have invoices, and only their owner reads them.
	for name, tc := range map[string]struct {
		url  string
		auth map[string]string
	}{
		"a checking account":  {accounts + "/" + bank + "/invoices", asAna},
		"another user's card": {invoices, asBob},
		"no account":          {accounts + "/" + bson.NewObjectID().Hex() + "/invoices", asAna},
	} {
		r := call(t, native, http.MethodGet, tc.url, nil, tc.auth)
		assert.Equal(t, http.StatusNotFound, r.status, name)
		assert.Equal(t, "not_found", r.str("code"), name)
	}
	r = call(t, native, http.MethodGet, invoices+"?from=2026-01&to=2028-01", nil, asAna)
	require.Equal(t, http.StatusUnprocessableEntity, r.status)
	assert.Equal(t, map[string]string{"to": "out_of_range"}, fieldErrors(r), "at most 24 invoices a read")

	// DELETE /me erases the card's transactions with the rest.
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
