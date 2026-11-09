package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

type fakeBudgets struct {
	usage      domain.BudgetUsage
	month      domain.BudgetMonth
	err        error
	userID, id string
	asOf       domain.Date
	listMonth  domain.Month
	input      domain.BudgetInput
	patch      domain.BudgetPatch
	deleted    bool
}

func (f *fakeBudgets) List(_ context.Context, userID string, month domain.Month, asOf domain.Date) (domain.BudgetMonth, error) {
	f.userID, f.listMonth, f.asOf = userID, month, asOf
	return f.month, f.err
}

func (f *fakeBudgets) Create(_ context.Context, userID string, in domain.BudgetInput, asOf domain.Date) (domain.BudgetUsage, error) {
	f.userID, f.input, f.asOf = userID, in, asOf
	return f.usage, f.err
}

func (f *fakeBudgets) Get(_ context.Context, userID, id string, asOf domain.Date) (domain.BudgetUsage, error) {
	f.userID, f.id, f.asOf = userID, id, asOf
	return f.usage, f.err
}

func (f *fakeBudgets) Update(_ context.Context, userID, id string, p domain.BudgetPatch, asOf domain.Date) (domain.BudgetUsage, error) {
	f.userID, f.id, f.patch, f.asOf = userID, id, p, asOf
	return f.usage, f.err
}

func (f *fakeBudgets) Delete(_ context.Context, userID, id string) error {
	f.userID, f.id, f.deleted = userID, id, f.err == nil
	return f.err
}

// serveBudgets routes r through the budgets routes as user u1.
func serveBudgets(svc *fakeBudgets, r *http.Request) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	(&Budgets{Service: svc, Logger: slog.New(slog.DiscardHandler)}).Mount(router)
	asUser(r)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func sampleBudget() domain.BudgetUsage {
	warned := t0
	return domain.BudgetUsage{
		Budget: domain.Budget{
			ID: "b1", UserID: "u1", CategoryID: "c1", Month: domain.NewMonth(2026, time.November), Currency: "BRL",
			Limit: 100000, WarnAt: 80, WarnedAt: &warned, CreatedAt: t0, UpdatedAt: t0,
		},
		Spent: 85000, Scheduled: 12000,
	}
}

var sampleBudgetJSON = map[string]any{
	"id": "b1", "categoryId": "c1", "month": "2026-11", "currency": "BRL",
	"limit": float64(100000), "warnAt": float64(80), "spent": float64(85000), "scheduled": float64(12000),
	"remaining": float64(15000), "status": "warning",
	"createdAt": "2026-11-08T12:00:00Z", "updatedAt": "2026-11-08T12:00:00Z",
}

func TestCreateBudget(t *testing.T) {
	svc := &fakeBudgets{usage: sampleBudget()}
	w := serveBudgets(svc, request(http.MethodPost, "/budgets?asOf=2026-11-09", `{"categoryId":"c1","currency":"BRL","limit":100000,"warnAt":80,"month":"2026-11"}`))
	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "/api/v1/budgets/b1", w.Header().Get("Location"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "u1", svc.userID, "the owner comes from the token")
	assert.Equal(t, "2026-11-09", svc.asOf.String())
	warnAt := 80
	assert.Equal(t, domain.BudgetInput{CategoryID: "c1", Month: "2026-11", Currency: "BRL", Limit: 100000, WarnAt: &warnAt}, svc.input)
	assert.Equal(t, sampleBudgetJSON, jsonOf(t, w), "no owner id and no alert marks in the answer")

	for name, body := range map[string]string{
		"owner in the body": `{"categoryId":"c1","userId":"u2"}`,
		"alert mark":        `{"categoryId":"c1","warnedAt":"2026-11-01T00:00:00Z"}`,
		"operator key":      `{"categoryId":"c1","$set":{"user_id":"u2"}}`,
	} {
		svc := &fakeBudgets{}
		w := serveBudgets(svc, request(http.MethodPost, "/budgets", body))
		assert.Equal(t, http.StatusBadRequest, w.Code, name)
		assert.Empty(t, svc.userID, name)
	}

	w = serveBudgets(&fakeBudgets{}, request(http.MethodPost, "/budgets", `{"limit":"lots"}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	w = serveBudgets(&fakeBudgets{}, request(http.MethodPost, "/budgets?asOf=today", `{}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)

	w = serveBudgets(&fakeBudgets{err: domain.ErrBudgetExists}, request(http.MethodPost, "/budgets", `{"categoryId":"c1","currency":"BRL","limit":1}`))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "budget_exists", problemOf(t, w).Code)

	svc = &fakeBudgets{err: &domain.ValidationError{Fields: []domain.FieldError{{Field: "categoryId", Code: domain.CodeKindMismatch}}}}
	w = serveBudgets(svc, request(http.MethodPost, "/budgets", `{"categoryId":"c9","currency":"BRL","limit":1}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	p := problemOf(t, w)
	require.Len(t, p.Errors, 1)
	assert.Equal(t, "categoryId", p.Errors[0].Field)
	assert.Equal(t, "kind_mismatch", p.Errors[0].Code)
}

func TestListBudgets(t *testing.T) {
	december := domain.NewMonth(2026, time.December)
	projected := sampleBudget()
	projected.Budget.ID, projected.Budget.Month = "", december
	svc := &fakeBudgets{month: domain.BudgetMonth{
		Month: december, Projected: true, Budgets: []domain.BudgetUsage{projected},
		Totals: []domain.BudgetTotal{{Currency: "BRL", Limit: 100000, Spent: 85000, Scheduled: 12000}},
	}}
	w := serveBudgets(svc, request(http.MethodGet, "/budgets?month=2026-12&asOf=2026-11-09", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, december, svc.listMonth)
	assert.Equal(t, "2026-11-09", svc.asOf.String())
	body := jsonOf(t, w)
	assert.Equal(t, "2026-12", body["month"])
	assert.Equal(t, true, body["projected"])
	budgets, ok := body["budgets"].([]any)
	require.True(t, ok)
	require.Len(t, budgets, 1)
	item, ok := budgets[0].(map[string]any)
	require.True(t, ok)
	assert.Nil(t, item["id"], "a projected budget has no id")
	assert.Equal(t, "2026-12", item["month"])
	assert.Equal(t, []any{map[string]any{
		"currency": "BRL", "limit": float64(100000), "spent": float64(85000), "scheduled": float64(12000), "remaining": float64(15000),
	}}, body["totals"])

	// The reader's month by default, and empty lists, never null.
	svc = &fakeBudgets{month: domain.BudgetMonth{Month: domain.NewMonth(2026, time.November)}}
	w = serveBudgets(svc, request(http.MethodGet, "/budgets", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, svc.listMonth.IsZero())
	assert.Equal(t, map[string]any{"month": "2026-11", "projected": false, "budgets": []any{}, "totals": []any{}}, jsonOf(t, w))

	for _, q := range []string{"month=2026-13", "month=11-2026", "asOf=2026-11"} {
		w = serveBudgets(&fakeBudgets{}, request(http.MethodGet, "/budgets?"+q, ""))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code, q)
	}

	w = serveBudgets(&fakeBudgets{err: errors.New("mongo down")}, request(http.MethodGet, "/budgets", ""))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGetUpdateDeleteBudget(t *testing.T) {
	svc := &fakeBudgets{usage: sampleBudget()}
	w := serveBudgets(svc, request(http.MethodGet, "/budgets/b1?asOf=2026-11-09", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "b1", svc.id)
	assert.Equal(t, sampleBudgetJSON, jsonOf(t, w))

	w = serveBudgets(svc, request(http.MethodPatch, "/budgets/b1", `{"limit":200000,"warnAt":null}`))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, svc.patch.Limit)
	assert.Equal(t, int64(200000), *svc.patch.Limit)
	assert.Nil(t, svc.patch.WarnAt, "null leaves the field as it is")

	for _, body := range []string{`{"categoryId":"c2"}`, `{"month":"2026-10"}`, `{"currency":"USD"}`} {
		svc := &fakeBudgets{}
		w := serveBudgets(svc, request(http.MethodPatch, "/budgets/b1", body))
		assert.Equal(t, http.StatusBadRequest, w.Code, body)
		assert.Empty(t, svc.userID, body)
	}

	w = serveBudgets(svc, request(http.MethodDelete, "/budgets/b1", ""))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.True(t, svc.deleted)

	for _, r := range []*http.Request{
		request(http.MethodGet, "/budgets/b9", ""),
		request(http.MethodPatch, "/budgets/b9", `{"limit":1}`),
		request(http.MethodDelete, "/budgets/b9", ""),
	} {
		w = serveBudgets(&fakeBudgets{err: domain.ErrNotFound}, r)
		assert.Equal(t, http.StatusNotFound, w.Code, r.Method)
	}
}

func TestBudgetsNeedASession(t *testing.T) {
	router := chi.NewRouter()
	(&Budgets{Service: &fakeBudgets{}, Logger: slog.New(slog.DiscardHandler)}).Mount(router)
	for _, r := range []*http.Request{
		request(http.MethodGet, "/budgets", ""),
		request(http.MethodPost, "/budgets", `{}`),
		request(http.MethodGet, "/budgets/b1", ""),
		request(http.MethodPatch, "/budgets/b1", `{}`),
		request(http.MethodDelete, "/budgets/b1", ""),
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		assert.Equal(t, http.StatusUnauthorized, w.Code, r.Method+" "+r.URL.Path)
	}
}
