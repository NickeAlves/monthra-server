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

type fakeRecurrences struct {
	rec        domain.Recurrence
	list       []domain.Recurrence
	upcoming   []domain.Transaction
	err        error
	userID, id string
	asOf, to   domain.Date
	input      domain.RecurrenceInput
	patch      domain.RecurrencePatch
	deleted    bool
}

func (f *fakeRecurrences) List(_ context.Context, userID string, asOf domain.Date) ([]domain.Recurrence, error) {
	f.userID, f.asOf = userID, asOf
	return f.list, f.err
}

func (f *fakeRecurrences) Create(_ context.Context, userID string, in domain.RecurrenceInput, asOf domain.Date) (domain.Recurrence, error) {
	f.userID, f.input, f.asOf = userID, in, asOf
	return f.rec, f.err
}

func (f *fakeRecurrences) Get(_ context.Context, userID, id string, asOf domain.Date) (domain.Recurrence, error) {
	f.userID, f.id, f.asOf = userID, id, asOf
	return f.rec, f.err
}

func (f *fakeRecurrences) Update(_ context.Context, userID, id string, p domain.RecurrencePatch, asOf domain.Date) (domain.Recurrence, error) {
	f.userID, f.id, f.patch, f.asOf = userID, id, p, asOf
	return f.rec, f.err
}

func (f *fakeRecurrences) Delete(_ context.Context, userID, id string) error {
	f.userID, f.id, f.deleted = userID, id, f.err == nil
	return f.err
}

func (f *fakeRecurrences) Upcoming(_ context.Context, userID string, to, asOf domain.Date) ([]domain.Transaction, error) {
	f.userID, f.to, f.asOf = userID, to, asOf
	return f.upcoming, f.err
}

// serveRecurrences routes r through the recurrences routes as user u1.
func serveRecurrences(svc *fakeRecurrences, r *http.Request) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	(&Recurrences{Service: svc, Logger: slog.New(slog.DiscardHandler)}).Mount(router)
	asUser(r)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func sampleRent(t *testing.T) domain.Recurrence {
	return domain.Recurrence{
		ID: "r1", UserID: "u1", Kind: domain.TransactionExpense, AccountID: "a1", Currency: "BRL", CategoryID: "c1",
		Amount: 250000, Description: "Rent", Tags: []string{"home"},
		Rule: domain.RecurrenceRule{Frequency: domain.FrequencyMonthly, Interval: 1, Start: day(t, "2026-08-05")},
		Next: day(t, "2026-12-05"), Last: day(t, "2026-11-05"), Source: domain.SourceManual, CreatedAt: t0, UpdatedAt: t0,
	}
}

func TestCreateRecurrence(t *testing.T) {
	svc := &fakeRecurrences{rec: sampleRent(t)}
	body := `{"kind":"expense","accountId":"a1","categoryId":"c1","amount":250000,"description":"Rent","tags":["home"],"frequency":"monthly","interval":1,"startDate":"2026-08-05","count":12}`
	w := serveRecurrences(svc, request(http.MethodPost, "/recurrences?asOf=2026-11-09", body))

	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "/api/v1/recurrences/r1", w.Header().Get("Location"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "u1", svc.userID, "the owner comes from the token")
	assert.Equal(t, "2026-11-09", svc.asOf.String())
	assert.Equal(t, domain.RecurrenceInput{
		Kind: "expense", AccountID: "a1", CategoryID: "c1", Amount: 250000, Description: "Rent", Tags: []string{"home"},
		Frequency: "monthly", Interval: 1, StartDate: "2026-08-05", Count: 12,
	}, svc.input)
	assert.Equal(t, map[string]any{
		"id": "r1", "kind": "expense", "accountId": "a1", "currency": "BRL", "categoryId": "c1",
		"toAccountId": nil, "toCurrency": nil, "amount": float64(250000), "toAmount": nil,
		"description": "Rent", "tags": []any{"home"}, "frequency": "monthly", "interval": float64(1),
		"startDate": "2026-08-05", "endDate": nil, "nextDate": "2026-12-05", "lastDate": "2026-11-05",
		"source": "manual", "createdAt": "2026-11-08T12:00:00Z", "updatedAt": "2026-11-08T12:00:00Z",
	}, jsonOf(t, w), "no owner id in the answer")

	for name, body := range map[string]string{
		"owner in the body": `{"kind":"expense","userId":"u2"}`,
		"next day":          `{"kind":"expense","nextDate":"2026-01-01"}`,
		"operator key":      `{"kind":"expense","$set":{"user_id":"u2"}}`,
	} {
		svc := &fakeRecurrences{}
		w := serveRecurrences(svc, request(http.MethodPost, "/recurrences", body))
		assert.Equal(t, http.StatusBadRequest, w.Code, name)
		assert.Empty(t, svc.userID, name)
	}
}

func TestRecurrenceViewOfATransfer(t *testing.T) {
	rec := sampleRent(t)
	rec.Kind, rec.CategoryID, rec.ToAccountID, rec.ToCurrency, rec.ToAmount = domain.TransactionTransfer, "", "a2", "USD", 9000
	rec.Rule.End, rec.Next, rec.Last, rec.Tags = day(t, "2026-11-30"), domain.Date{}, domain.Date{}, nil
	w := serveRecurrences(&fakeRecurrences{rec: rec}, request(http.MethodGet, "/recurrences/r1", ""))
	require.Equal(t, http.StatusOK, w.Code)
	body := jsonOf(t, w)
	assert.Nil(t, body["categoryId"])
	assert.Equal(t, "a2", body["toAccountId"])
	assert.Equal(t, float64(9000), body["toAmount"])
	assert.Equal(t, "2026-11-30", body["endDate"])
	assert.Nil(t, body["nextDate"], "ended")
	assert.Nil(t, body["lastDate"], "none created")
	assert.Equal(t, []any{}, body["tags"], "never null")
}

func TestRecurrenceRoutes(t *testing.T) {
	svc := &fakeRecurrences{list: []domain.Recurrence{sampleRent(t)}}
	w := serveRecurrences(svc, request(http.MethodGet, "/recurrences?asOf=2026-11-09", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Len(t, jsonOf(t, w)["recurrences"], 1)
	assert.Equal(t, "2026-11-09", svc.asOf.String())

	svc = &fakeRecurrences{}
	w = serveRecurrences(svc, request(http.MethodGet, "/recurrences", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []any{}, jsonOf(t, w)["recurrences"], "never null")

	svc = &fakeRecurrences{rec: sampleRent(t)}
	w = serveRecurrences(svc, request(http.MethodGet, "/recurrences/r1?asOf=2026-11-07", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "r1", svc.id)
	assert.Equal(t, "2026-11-07", svc.asOf.String())

	svc = &fakeRecurrences{rec: sampleRent(t)}
	w = serveRecurrences(svc, request(http.MethodPatch, "/recurrences/r1", `{"amount":270000,"startDate":"2026-12-10","endDate":""}`))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, svc.patch.Amount)
	assert.Equal(t, int64(270000), *svc.patch.Amount)
	require.NotNil(t, svc.patch.EndDate)
	assert.Empty(t, *svc.patch.EndDate, "empty removes the end")
	assert.Nil(t, svc.patch.Frequency)
	w = serveRecurrences(svc, request(http.MethodPatch, "/recurrences/r1", `{"kind":"income"}`))
	assert.Equal(t, http.StatusBadRequest, w.Code, "the kind never changes")

	svc = &fakeRecurrences{}
	w = serveRecurrences(svc, request(http.MethodDelete, "/recurrences/r1", ""))
	require.Equal(t, http.StatusNoContent, w.Code)
	assert.True(t, svc.deleted)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/recurrences", ""},
		{http.MethodPost, "/recurrences", `{"kind":"expense"}`},
		{http.MethodGet, "/recurrences/r1", ""},
		{http.MethodPatch, "/recurrences/r1", `{}`},
	} {
		svc := &fakeRecurrences{}
		w := serveRecurrences(svc, request(tc.method, tc.path+"?asOf=2026-13-01", tc.body))
		require.Equal(t, http.StatusUnprocessableEntity, w.Code, tc.method+" "+tc.path)
		assert.Equal(t, "asOf", problemOf(t, w).Errors[0].Field)
		assert.Empty(t, svc.userID)
	}

	for err, want := range map[error]struct {
		status int
		code   string
	}{
		domain.ErrNotFound:     {http.StatusNotFound, "not_found"},
		domain.ErrLimitReached: {http.StatusConflict, "limit_reached"},
		errors.New("boom"):     {http.StatusInternalServerError, "internal_error"},
	} {
		for _, tc := range []struct{ method, path, body string }{
			{http.MethodGet, "/recurrences", ""},
			{http.MethodPost, "/recurrences", `{"kind":"expense"}`},
			{http.MethodGet, "/recurrences/r1", ""},
			{http.MethodPatch, "/recurrences/r1", `{}`},
			{http.MethodDelete, "/recurrences/r1", ""},
			{http.MethodGet, "/recurrences/upcoming", ""},
		} {
			w := serveRecurrences(&fakeRecurrences{err: err}, request(tc.method, tc.path, tc.body))
			assert.Equal(t, want.status, w.Code, "%v %s %s", err, tc.method, tc.path)
			assert.Equal(t, want.code, problemOf(t, w).Code)
		}
	}
}

func TestUpcomingRecurrences(t *testing.T) {
	dec, err := domain.ParseMonth("2026-12")
	require.NoError(t, err)
	svc := &fakeRecurrences{upcoming: []domain.Transaction{{
		Kind: domain.TransactionExpense, AccountID: "card", Currency: "BRL", CategoryID: "c1", Amount: 3990,
		Date: day(t, "2026-11-24"), InvoiceMonth: dec, Description: "Streaming",
		Recurrence: &domain.RecurrenceLink{ID: "r2", Date: day(t, "2026-11-24")},
	}}}
	w := serveRecurrences(svc, request(http.MethodGet, "/recurrences/upcoming?to=2026-12-31&asOf=2026-11-09", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "2026-12-31", svc.to.String())
	assert.Equal(t, "2026-11-09", svc.asOf.String())
	assert.Equal(t, []any{map[string]any{
		"recurrenceId": "r2", "date": "2026-11-24", "kind": "expense", "accountId": "card", "currency": "BRL",
		"categoryId": "c1", "toAccountId": nil, "toCurrency": nil, "amount": float64(3990), "toAmount": nil,
		"description": "Streaming", "tags": []any{}, "invoiceMonth": "2026-12", "toInvoiceMonth": nil,
	}}, jsonOf(t, w)["occurrences"])

	svc = &fakeRecurrences{}
	w = serveRecurrences(svc, request(http.MethodGet, "/recurrences/upcoming", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []any{}, jsonOf(t, w)["occurrences"])
	assert.True(t, svc.to.IsZero(), "the use case picks the default")

	svc = &fakeRecurrences{}
	w = serveRecurrences(svc, request(http.MethodGet, "/recurrences/upcoming?to=soon&asOf=today", ""))
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Len(t, problemOf(t, w).Errors, 2)
	assert.Empty(t, svc.userID)
}

func TestUsedByRecurrence(t *testing.T) {
	w := serveAccounts(&fakeAccounts{err: domain.ErrUsedByRecurrence}, request(http.MethodPatch, "/accounts/a1", `{"archived":true}`))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "used_by_recurrence", problemOf(t, w).Code)
}
