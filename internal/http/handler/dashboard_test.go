package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

type fakeDashboard struct {
	sum    domain.Summary
	err    error
	userID string
	month  domain.Month
	asOf   domain.Date
}

func (f *fakeDashboard) Summary(_ context.Context, userID string, month domain.Month, asOf domain.Date) (domain.Summary, error) {
	f.userID, f.month, f.asOf = userID, month, asOf
	return f.sum, f.err
}

// serveDashboard routes r through the dashboard routes as user u1.
func serveDashboard(svc *fakeDashboard, r *http.Request) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	(&Dashboard{Service: svc, Logger: slog.New(slog.DiscardHandler)}).Mount(router)
	asUser(r)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestDashboardSummary(t *testing.T) {
	nov, err := domain.ParseMonth("2026-11")
	require.NoError(t, err)
	worth := int64(97000)
	svc := &fakeDashboard{sum: domain.Summary{
		Month: nov, AsOf: day(t, "2026-11-08"),
		Currencies: []domain.CurrencySummary{{
			Currency: "BRL", Income: 850000, Expense: 3000, Net: 847000, Scheduled: domain.Flows{Expense: 500}, Balance: worth,
			ExpensesByCategory: []domain.CategoryTotal{{CategoryID: "food", Amount: 3000, Count: 2}},
			IncomeByCategory:   []domain.CategoryTotal{},
			History: []domain.SummaryPoint{
				{Month: nov, Income: 850000, Expense: 3000, NetWorth: &worth},
				{Month: nov.AddMonths(1)},
			},
		}},
		Accounts: []domain.AccountBalance{{AccountID: "bank", Currency: "BRL", Balance: worth}},
	}}
	w := serveDashboard(svc, request(http.MethodGet, "/dashboard/summary?month=2026-11&asOf=2026-11-08", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "u1", svc.userID)
	assert.Equal(t, nov, svc.month)
	assert.Equal(t, "2026-11-08", svc.asOf.String())
	assert.Equal(t, map[string]any{
		"month": "2026-11", "asOf": "2026-11-08",
		"currencies": []any{map[string]any{
			"currency": "BRL", "income": float64(850000), "expense": float64(3000), "net": float64(847000),
			"scheduled":          map[string]any{"income": float64(0), "expense": float64(500)},
			"balance":            float64(97000),
			"expensesByCategory": []any{map[string]any{"categoryId": "food", "amount": float64(3000), "count": float64(2)}},
			"incomeByCategory":   []any{},
			"history": []any{
				map[string]any{"month": "2026-11", "income": float64(850000), "expense": float64(3000), "netWorth": float64(97000)},
				map[string]any{"month": "2026-12", "income": float64(0), "expense": float64(0), "netWorth": nil},
			},
		}},
		"accounts": []any{map[string]any{"accountId": "bank", "currency": "BRL", "balance": float64(97000)}},
	}, jsonOf(t, w))

	// Without a month, the use case picks the reader's.
	svc = &fakeDashboard{}
	w = serveDashboard(svc, request(http.MethodGet, "/dashboard/summary", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, svc.month.IsZero())
	assert.True(t, svc.asOf.IsZero())
	body := jsonOf(t, w)
	assert.Equal(t, []any{}, body["currencies"])
	assert.Equal(t, []any{}, body["accounts"])

	svc = &fakeDashboard{}
	w = serveDashboard(svc, request(http.MethodGet, "/dashboard/summary?month=2026-13&asOf=today", ""))
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Len(t, problemOf(t, w).Errors, 2)
	assert.Empty(t, svc.userID)

	w = serveDashboard(&fakeDashboard{err: errors.New("mongo down")}, request(http.MethodGet, "/dashboard/summary", ""))
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	w = httptest.NewRecorder()
	(&Dashboard{Service: &fakeDashboard{}, Logger: slog.New(slog.DiscardHandler)}).Summary(w, request(http.MethodGet, "/dashboard/summary", ""))
	assert.Equal(t, http.StatusUnauthorized, w.Code, "no principal")
}
