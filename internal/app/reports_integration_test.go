//go:build integration

package app

import (
	"context"
	"io"
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

// download GETs url and returns the raw answer.
func download(t *testing.T, client *http.Client, url string, headers map[string]string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, string(body)
}

// listOf returns the list at key of c.
func listOf(c map[string]any, key string) []any {
	list, _ := c[key].([]any)
	return list
}

func TestReportsEndToEnd(t *testing.T) {
	t.Setenv("RATE_LIMIT_API_USER", "1000/m")
	t.Setenv("RATE_LIMIT_EXPORT_USER", "4/h")
	api, mail, mongoURI, metricsURL := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const ana, bob = "ana@example.com", "bob@example.com"
	password := strings.Repeat("correct horse ", 2)
	signUp(t, api, mail, ana, password)
	signUp(t, api, mail, bob, password)
	asAna := signIn(t, native, api, ana, password)
	asBob := signIn(t, native, api, bob, password)
	// The server reads up to today: the days are relative.
	today := domain.DateOf(time.Now().UTC())
	month := domain.MonthOf(today)
	first := month.AddMonths(-2)

	create := func(auth map[string]string, path string, body map[string]any) string {
		t.Helper()
		r := call(t, native, http.MethodPost, api+"/api/v1"+path, body, auth)
		require.Equal(t, http.StatusCreated, r.status, r.body)
		return r.str("id")
	}
	bank := create(asAna, "/accounts", map[string]any{"name": "Bank", "type": "checking", "currency": "BRL", "initialBalance": 1000000})
	card := create(asAna, "/accounts", map[string]any{
		"name": "Card", "type": "credit_card", "currency": "BRL", "card": map[string]any{"closingDay": 25, "dueDay": 5},
	})
	usd := create(asAna, "/accounts", map[string]any{"name": "Dollars", "type": "checking", "currency": "USD", "initialBalance": 50000})
	categories := byKey(mustList(t, native, api+"/api/v1/categories", asAna))
	groceries, shopping, salary := categories["groceries"].ID, categories["shopping"].ID, categories["salary"].ID

	// The period before: May to July when the report covers August to October.
	create(asAna, "/transactions", map[string]any{
		"kind": "expense", "accountId": bank, "categoryId": groceries, "amount": 20000, "date": month.AddMonths(-3).Day(10).String(),
	})
	create(asAna, "/transactions", map[string]any{
		"kind": "income", "accountId": bank, "categoryId": salary, "amount": 850000, "date": first.Day(1).String(), "tags": []string{"work"},
	})
	create(asAna, "/transactions", map[string]any{
		"kind": "expense", "accountId": bank, "categoryId": groceries, "amount": 30000, "date": month.AddMonths(-1).Day(15).String(),
		"description": "=SUM(A1)", "tags": []string{"home", "weekly"},
	})
	// Three installments on the card from the period's first day: one in each month.
	create(asAna, "/transactions", map[string]any{
		"kind": "expense", "accountId": card, "categoryId": shopping, "amount": 90000, "date": first.Day(1).String(), "installments": 3,
	})
	// A transfer between currencies is no flow.
	create(asAna, "/transactions", map[string]any{
		"kind": "transfer", "accountId": bank, "toAccountId": usd, "amount": 10000, "toAmount": 2000, "date": today.String(),
	})
	// A monthly subscription from last month: two occurrences.
	create(asAna, "/recurrences", map[string]any{
		"kind": "expense", "accountId": bank, "categoryId": categories["subscriptions"].ID, "amount": 5000,
		"frequency": "monthly", "startDate": month.AddMonths(-1).Day(1).String(),
	})
	// What an investment paid counts as income, in no account.
	fund := create(asAna, "/investments", map[string]any{"name": "Fund", "type": "fund", "currency": "BRL"})
	create(asAna, "/investments/"+fund+"/movements", map[string]any{
		"type": "income", "date": today.String(), "amount": 1500, "categoryId": categories["investment_income"].ID,
	})
	// Bob's never show in Ana's.
	bobBank := create(asBob, "/accounts", map[string]any{"name": "Bob's", "type": "checking", "currency": "BRL"})
	bobGroceries := byKey(mustList(t, native, api+"/api/v1/categories", asBob))["groceries"].ID
	create(asBob, "/transactions", map[string]any{
		"kind": "expense", "accountId": bobBank, "categoryId": bobGroceries, "amount": 777, "date": today.String(), "tags": []string{"home"},
	})

	summary := api + "/api/v1/reports/summary"
	r := call(t, native, http.MethodGet, summary+"?from="+first.String()+"&to="+month.String()+"&asOf="+today.String(), nil, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	assert.Equal(t, first.String(), r.str("from"))
	assert.Equal(t, month.String(), r.str("to"))
	assert.Equal(t, today.String(), r.str("asOf"))
	assert.Equal(t, map[string]any{"from": month.AddMonths(-5).String(), "to": month.AddMonths(-3).String()}, r.body["previous"])

	brl := currencyOf(t, r, "BRL")
	assert.Equal(t, float64(851500), brl["income"], "the salary and the fund's income")
	assert.Equal(t, float64(130000), brl["expense"], "groceries, the installments and the occurrences")
	assert.Equal(t, float64(721500), brl["net"])
	assert.Equal(t, float64(8473), brl["savingsRateBps"])
	assert.Equal(t, float64(1500), brl["investmentIncome"])
	assert.Equal(t, map[string]any{"income": float64(0), "expense": float64(20000), "net": float64(-20000), "savingsRateBps": nil}, brl["previous"])
	assert.Equal(t, []any{
		map[string]any{"month": first.String(), "income": float64(850000), "expense": float64(30000), "net": float64(820000), "netWorth": float64(1800000)},
		map[string]any{"month": month.AddMonths(-1).String(), "income": float64(0), "expense": float64(65000), "net": float64(-65000), "netWorth": float64(1735000)},
		map[string]any{"month": month.String(), "income": float64(1500), "expense": float64(35000), "net": float64(-33500), "netWorth": float64(1690000)},
	}, brl["months"], "the net worth counts what came before the period, and leaves the portfolio out")
	assert.Equal(t, []any{
		map[string]any{"categoryId": shopping, "amount": float64(90000), "count": float64(3), "previousAmount": float64(0)},
		map[string]any{"categoryId": groceries, "amount": float64(30000), "count": float64(1), "previousAmount": float64(20000)},
		map[string]any{"categoryId": categories["subscriptions"].ID, "amount": float64(10000), "count": float64(2), "previousAmount": float64(0)},
	}, brl["expensesByCategory"])
	assert.Len(t, listOf(brl, "incomeByCategory"), 2)
	assert.Equal(t, []any{
		map[string]any{"accountId": bank, "income": float64(850000), "expense": float64(40000), "count": float64(4)},
		map[string]any{"accountId": card, "income": float64(0), "expense": float64(90000), "count": float64(3)},
	}, brl["byAccount"])
	assert.Equal(t, []any{
		map[string]any{"tag": "home", "amount": float64(30000), "count": float64(1)},
		map[string]any{"tag": "weekly", "amount": float64(30000), "count": float64(1)},
	}, brl["expensesByTag"])
	assert.Equal(t, []any{map[string]any{"tag": "work", "amount": float64(850000), "count": float64(1)}}, brl["incomeByTag"])
	usdReport := currencyOf(t, r, "USD")
	assert.Equal(t, float64(0), usdReport["income"])
	assert.Equal(t, float64(52000), listOf(usdReport, "months")[2].(map[string]any)["netWorth"])

	// The defaults, and the bounds of a period.
	r = call(t, native, http.MethodGet, summary, nil, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Equal(t, month.AddMonths(-11).String(), r.str("from"))
	assert.Len(t, listOf(currencyOf(t, r, "BRL"), "months"), 12)
	r = call(t, native, http.MethodGet, summary+"?from="+month.String()+"&to="+first.String(), nil, asAna)
	assert.Equal(t, http.StatusUnprocessableEntity, r.status)
	r = call(t, native, http.MethodGet, summary+"?from="+month.AddMonths(-60).String()+"&to="+month.String(), nil, asAna)
	assert.Equal(t, http.StatusUnprocessableEntity, r.status, "61 months")

	// Bob's report holds his own only.
	r = call(t, native, http.MethodGet, summary, nil, asBob)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, float64(777), currencyOf(t, r, "BRL")["expense"])

	// The export: CSV in the caller's stored locale (pt-BR by default).
	export := api + "/api/v1/reports/export?format=csv&from=" + first.Day(1).String() + "&to=" + today.String()
	status, _, body := download(t, native, api+"/api/v1/reports/export?format=pdf", asAna)
	assert.Equal(t, http.StatusUnprocessableEntity, status, body)
	status, header, body := download(t, native, export, asAna)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "text/csv; charset=utf-8", header.Get("Content-Type"))
	assert.Equal(t, `attachment; filename="monthra-transactions-`+today.String()+`.csv"`, header.Get("Content-Disposition"))
	assert.Equal(t, "no-store", header.Get("Cache-Control"))
	lines := strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n")
	require.Len(t, lines, 9, "a header and the 8 transactions of the days asked for")
	assert.Equal(t, "\ufeffData;Tipo;Descrição;Categoria;Conta;Valor;Moeda;Conta de destino;Valor de destino;Moeda de destino;Tags;Parcela;Fatura do cartão", lines[0])
	ddmmyyyy := func(d domain.Date) string { return d.Time().Format("02/01/2006") }
	assert.Contains(t, lines, ddmmyyyy(month.AddMonths(-1).Day(15))+";Despesa;'=SUM(A1);Mercado;Bank;-300,00;BRL;;;;home, weekly;;",
		"a formula is kept as text")
	assert.Contains(t, lines, ddmmyyyy(today)+";Transferência;;;Bank;-100,00;BRL;Dollars;20,00;USD;;;")
	assert.Contains(t, body, ";1/3;", "the installment")
	assert.NotContains(t, body, "-1000,00", "the period before is out")
	assert.NotContains(t, body, "7,77", "another user's")

	r = call(t, native, http.MethodPatch, api+"/api/v1/me/preferences", map[string]any{"locale": "en-US"}, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	status, _, body = download(t, native, export+"&tag=home", asAna)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "\ufeffDate,Type,Description,Category,Account,Amount,Currency,Destination account,Destination amount,Destination currency,Tags,Installment,Card invoice\r\n"+
		month.AddMonths(-1).Day(15).String()+",Expense,'=SUM(A1),Groceries,Bank,-300.00,BRL,,,,\"home, weekly\",,\r\n", body,
		"the list's filters; en-US now")

	status, _, body = download(t, native, api+"/api/v1/reports/export?format=csv", asBob)
	require.Equal(t, http.StatusOK, status, body)
	assert.Len(t, strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n"), 2, "Bob's one transaction")

	// The fifth export within the hour (the refused one counted) is over the budget; Bob's is apart.
	status, _, _ = download(t, native, export, asAna)
	require.Equal(t, http.StatusOK, status)
	status, header, _ = download(t, native, export, asAna)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.NotEmpty(t, header.Get("Retry-After"))
	r = call(t, native, http.MethodGet, summary, nil, asAna)
	assert.Equal(t, http.StatusOK, r.status, "the reports have their own budget")

	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	anaID := call(t, native, http.MethodGet, api+"/api/v1/me", nil, asAna).str("id")
	oid, err := bson.ObjectIDFromHex(anaID)
	require.NoError(t, err)
	n, err := client.Database("monthra").Collection(repomongo.CollAuditLogs).CountDocuments(ctx,
		bson.D{{Key: "user_id", Value: oid}, {Key: "event", Value: "transactions_exported"}, {Key: "reason", Value: "csv"}})
	require.NoError(t, err)
	assert.Equal(t, int64(3), n, "each export Ana got is audited")

	status, body = get(t, metricsURL)
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, `monthra_exports_total{dataset="transactions"} 4`)
	assert.Contains(t, body, `monthra_ratelimit_rejected_total{policy="export_user"} 1`)
}
