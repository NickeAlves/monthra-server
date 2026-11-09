//go:build integration

package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// currencyOf returns the part of summary r in currency code.
func currencyOf(t *testing.T, r response, code string) map[string]any {
	t.Helper()
	list, _ := r.body["currencies"].([]any)
	for _, raw := range list {
		c, _ := raw.(map[string]any)
		if c["currency"] == code {
			return c
		}
	}
	require.Failf(t, "no currency", "%s in %v", code, r.body)
	return nil
}

// historyOf returns point i of the history of c.
func historyOf(c map[string]any, i int) map[string]any {
	list, _ := c["history"].([]any)
	p, _ := list[i].(map[string]any)
	return p
}

func TestDashboardEndToEnd(t *testing.T) {
	t.Setenv("RATE_LIMIT_API_USER", "1000/m")
	api, mail, _, metricsURL := startApp(t)
	native := &http.Client{Timeout: 10 * time.Second}
	const ana, bob = "ana@example.com", "bob@example.com"
	password := strings.Repeat("correct horse ", 2)
	signUp(t, api, mail, ana, password)
	signUp(t, api, mail, bob, password)
	asAna := signIn(t, native, api, ana, password)
	asBob := signIn(t, native, api, bob, password)
	accounts, txs, recs := api+"/api/v1/accounts", api+"/api/v1/transactions", api+"/api/v1/recurrences"
	summary := api + "/api/v1/dashboard/summary"
	// The server sums up to today: the days are relative.
	today := domain.DateOf(time.Now().UTC())
	month := domain.MonthOf(today)

	create := func(url string, body map[string]any) response {
		t.Helper()
		r := call(t, native, http.MethodPost, url, body, asAna)
		require.Equal(t, http.StatusCreated, r.status, r.body)
		return r
	}
	bank := create(accounts, map[string]any{"name": "Bank", "type": "checking", "currency": "BRL", "initialBalance": 1000000}).str("id")
	card := create(accounts, map[string]any{
		"name": "Card", "type": "credit_card", "currency": "BRL", "card": map[string]any{"closingDay": 25, "dueDay": 5},
	}).str("id")
	usd := create(accounts, map[string]any{"name": "Dollars", "type": "checking", "currency": "USD", "initialBalance": 50000}).str("id")
	categories := byKey(mustList(t, native, api+"/api/v1/categories", asAna))
	groceries, shopping, salary := categories["groceries"].ID, categories["shopping"].ID, categories["salary"].ID

	first := month.Day(1).String()
	create(txs, map[string]any{"kind": "income", "accountId": bank, "categoryId": salary, "amount": 850000, "date": first})
	create(txs, map[string]any{"kind": "expense", "accountId": bank, "categoryId": groceries, "amount": 30000, "date": today.String()})
	// Three installments on the card from the 1st: one in each month.
	create(txs, map[string]any{"kind": "expense", "accountId": card, "categoryId": shopping, "amount": 90000, "date": first, "installments": 3})
	// A transfer between currencies is no flow.
	create(txs, map[string]any{"kind": "transfer", "accountId": bank, "toAccountId": usd, "amount": 10000, "toAmount": 2000, "date": today.String()})
	// A streaming from tomorrow: still to come.
	tomorrow := today.AddDays(1)
	create(recs, map[string]any{
		"kind": "expense", "accountId": bank, "categoryId": categories["subscriptions"].ID, "amount": 5000,
		"frequency": "monthly", "startDate": tomorrow.String(),
	})

	r := call(t, native, http.MethodGet, summary+"?asOf="+today.String(), nil, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Equal(t, "no-store", r.header.Get("Cache-Control"))
	assert.Equal(t, month.String(), r.str("month"))
	assert.Equal(t, today.String(), r.str("asOf"))
	brl := currencyOf(t, r, "BRL")
	assert.Equal(t, float64(850000), brl["income"])
	assert.Equal(t, float64(60000), brl["expense"], "the groceries and the first installment")
	assert.Equal(t, float64(790000), brl["net"])
	wantScheduled := float64(0)
	if domain.MonthOf(tomorrow) == month {
		wantScheduled = 5000
	}
	assert.Equal(t, map[string]any{"income": float64(0), "expense": wantScheduled}, brl["scheduled"])
	assert.Equal(t, float64(1000000+850000-30000-10000-30000), brl["balance"])
	byCategory, _ := brl["expensesByCategory"].([]any)
	assert.ElementsMatch(t, []any{
		map[string]any{"categoryId": shopping, "amount": float64(30000), "count": float64(1)},
		map[string]any{"categoryId": groceries, "amount": float64(30000), "count": float64(1)},
	}, byCategory)
	assert.Len(t, brl["history"], 12)
	assert.Equal(t, float64(1000000), historyOf(brl, 10)["netWorth"], "the initial balances before this month")
	assert.Equal(t, brl["balance"], historyOf(brl, 11)["netWorth"])
	usdPart := currencyOf(t, r, "USD")
	assert.Equal(t, float64(52000), usdPart["balance"])
	assert.Equal(t, float64(0), usdPart["income"])
	assert.Equal(t, []any{
		map[string]any{"accountId": bank, "currency": "BRL", "balance": float64(1000000 + 850000 - 30000 - 10000)},
		map[string]any{"accountId": card, "currency": "BRL", "balance": float64(-30000)},
		map[string]any{"accountId": usd, "currency": "USD", "balance": float64(52000)},
	}, r.body["accounts"])

	// The same summary again comes from the cache.
	again := call(t, native, http.MethodGet, summary+"?asOf="+today.String(), nil, asAna)
	require.Equal(t, http.StatusOK, again.status)
	assert.Equal(t, r.body, again.body)
	_, metrics := get(t, metricsURL)
	assert.Contains(t, metrics, `monthra_cache_requests_total{cache="summary",result="hit"} 1`)

	// A write shows at once: the version moved.
	create(txs, map[string]any{"kind": "expense", "accountId": bank, "categoryId": groceries, "amount": 1000, "date": today.String()})
	r = call(t, native, http.MethodGet, summary+"?asOf="+today.String(), nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, float64(61000), currencyOf(t, r, "BRL")["expense"])

	// Next month: its installment, the streaming to come, no net worth yet.
	next := month.AddMonths(1)
	r = call(t, native, http.MethodGet, summary+"?month="+next.String()+"&asOf="+today.String(), nil, asAna)
	require.Equal(t, http.StatusOK, r.status, r.body)
	assert.Equal(t, today.String(), r.str("asOf"))
	brl = currencyOf(t, r, "BRL")
	assert.Equal(t, float64(30000), brl["expense"])
	assert.Equal(t, float64(5000), brl["scheduled"].(map[string]any)["expense"])
	assert.Nil(t, historyOf(brl, 11)["netWorth"])
	assert.NotNil(t, historyOf(brl, 10)["netWorth"])

	// A past month counts up to its last day.
	prev := month.AddMonths(-1)
	r = call(t, native, http.MethodGet, summary+"?month="+prev.String(), nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, prev.LastDay().String(), r.str("asOf"))
	assert.Equal(t, float64(1000000), currencyOf(t, r, "BRL")["balance"])
	assert.Equal(t, float64(0), currencyOf(t, r, "BRL")["expense"])

	r = call(t, native, http.MethodGet, summary+"?month=2026-13", nil, asAna)
	require.Equal(t, http.StatusUnprocessableEntity, r.status)
	assert.Equal(t, map[string]string{"month": "invalid"}, fieldErrors(r))

	// Bob sees nothing of Ana's.
	r = call(t, native, http.MethodGet, summary, nil, asBob)
	require.Equal(t, http.StatusOK, r.status)
	assert.Equal(t, []any{}, r.body["currencies"])
	assert.Equal(t, []any{}, r.body["accounts"])

	// DELETE /me drops the version of the cached summaries.
	r = call(t, native, http.MethodGet, api+"/api/v1/me", nil, asAna)
	require.Equal(t, http.StatusOK, r.status)
	anaID := r.str("id")
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("REDIS_ADDR")})
	t.Cleanup(func() { _ = rdb.Close() })
	versionKey := fmt.Sprintf("monthra:local:summary:%s:ver", anaID)
	n, err := rdb.Exists(ctx, versionKey).Result()
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	r = call(t, native, http.MethodDelete, api+"/api/v1/me", map[string]string{"password": password}, asAna)
	require.Equal(t, http.StatusNoContent, r.status)
	n, err = rdb.Exists(ctx, versionKey).Result()
	require.NoError(t, err)
	assert.Zero(t, n)
}
