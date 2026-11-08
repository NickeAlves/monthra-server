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

type fakeAccounts struct {
	account         domain.Account
	err             error
	userID, id      string
	includeArchived bool
	input           domain.AccountInput
	patch           domain.AccountPatch
	deleted         bool
}

func (f *fakeAccounts) List(_ context.Context, userID string, includeArchived bool) ([]domain.Account, error) {
	f.userID, f.includeArchived = userID, includeArchived
	if f.err != nil {
		return nil, f.err
	}
	if f.account.ID == "" {
		return nil, nil
	}
	return []domain.Account{f.account}, nil
}

func (f *fakeAccounts) Create(_ context.Context, userID string, in domain.AccountInput) (domain.Account, error) {
	f.userID, f.input = userID, in
	return f.account, f.err
}

func (f *fakeAccounts) Get(_ context.Context, userID, id string) (domain.Account, error) {
	f.userID, f.id = userID, id
	return f.account, f.err
}

func (f *fakeAccounts) Update(_ context.Context, userID, id string, p domain.AccountPatch) (domain.Account, error) {
	f.userID, f.id, f.patch = userID, id, p
	return f.account, f.err
}

func (f *fakeAccounts) Delete(_ context.Context, userID, id string) error {
	f.userID, f.id, f.deleted = userID, id, f.err == nil
	return f.err
}

func sampleCard() domain.Account {
	limit := int64(500000)
	return domain.Account{
		ID: "a1", UserID: "u1", Name: "Card", Type: domain.AccountCreditCard, Currency: "BRL", InitialBalance: -1200,
		Card:   &domain.CardSettings{ClosingDay: 3, DueDay: 10, Limit: &limit},
		Source: domain.SourceManual, CreatedAt: t0, UpdatedAt: t0,
	}
}

// serveAccounts routes r through the accounts routes as user u1.
func serveAccounts(svc *fakeAccounts, r *http.Request) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	(&Accounts{Service: svc, Logger: slog.New(slog.DiscardHandler)}).Mount(router)
	asUser(r)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestCreateAccount(t *testing.T) {
	svc := &fakeAccounts{account: sampleCard()}
	body := `{"name":"Card","type":"credit_card","currency":"BRL","initialBalance":-1200,"card":{"closingDay":3,"dueDay":10,"limit":500000}}`
	w := serveAccounts(svc, request(http.MethodPost, "/accounts", body))

	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "/api/v1/accounts/a1", w.Header().Get("Location"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "u1", svc.userID, "the owner comes from the token")
	limit := int64(500000)
	assert.Equal(t, domain.AccountInput{
		Name: "Card", Type: "credit_card", Currency: "BRL", InitialBalance: -1200,
		Card: &domain.CardSettings{ClosingDay: 3, DueDay: 10, Limit: &limit},
	}, svc.input)
	assert.Equal(t, map[string]any{
		"id": "a1", "name": "Card", "type": "credit_card", "currency": "BRL", "initialBalance": float64(-1200),
		"card":   map[string]any{"closingDay": float64(3), "dueDay": float64(10), "limit": float64(500000)},
		"source": "manual", "archived": false,
		"createdAt": "2026-11-08T12:00:00Z", "updatedAt": "2026-11-08T12:00:00Z",
	}, jsonOf(t, w), "no owner id in the answer")
}

func TestCreateAccountRejectsUnsafeBodies(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"owner in the body":  {`{"name":"A","type":"cash","currency":"BRL","userId":"u2"}`, http.StatusBadRequest, "bad_request"},
		"operator key":       {`{"name":"A","type":"cash","currency":"BRL","$set":{"user_id":"u2"}}`, http.StatusBadRequest, "bad_request"},
		"amount as a float":  {`{"name":"A","type":"cash","currency":"BRL","initialBalance":10.5}`, http.StatusUnprocessableEntity, "validation_failed"},
		"amount as a string": {`{"name":"A","type":"cash","currency":"BRL","initialBalance":"10"}`, http.StatusUnprocessableEntity, "validation_failed"},
		"amount over int64":  {`{"name":"A","type":"cash","currency":"BRL","initialBalance":1e30}`, http.StatusUnprocessableEntity, "validation_failed"},
		"not json":           {`name=A`, http.StatusBadRequest, "bad_request"},
		"two objects":        {`{"name":"A"}{"name":"B"}`, http.StatusBadRequest, "bad_request"},
	} {
		svc := &fakeAccounts{}
		w := serveAccounts(svc, request(http.MethodPost, "/accounts", tc.body))
		assert.Equal(t, tc.status, w.Code, name)
		assert.Equal(t, tc.code, problemOf(t, w).Code, name)
		assert.Empty(t, svc.userID, "%s: the use case is never reached", name)
	}
}

func TestCreateAccountErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"invalid": {&domain.ValidationError{Fields: []domain.FieldError{{Field: "card", Code: domain.CodeRequired}}}, http.StatusUnprocessableEntity, "validation_failed"},
		"limit":   {domain.ErrLimitReached, http.StatusConflict, "limit_reached"},
		"down":    {errors.New("mongo down"), http.StatusInternalServerError, "internal_error"},
	} {
		w := serveAccounts(&fakeAccounts{err: tc.err}, request(http.MethodPost, "/accounts", `{"name":"A","type":"credit_card","currency":"BRL"}`))
		assert.Equal(t, tc.status, w.Code, name)
		p := problemOf(t, w)
		assert.Equal(t, tc.code, p.Code, name)
		if tc.status == http.StatusUnprocessableEntity {
			require.Len(t, p.Errors, 1, name)
			assert.Equal(t, "card", p.Errors[0].Field)
		}
	}
}

func TestListAccounts(t *testing.T) {
	svc := &fakeAccounts{account: sampleCard()}
	w := serveAccounts(svc, request(http.MethodGet, "/accounts", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.False(t, svc.includeArchived)
	list, _ := jsonOf(t, w)["accounts"].([]any)
	assert.Len(t, list, 1)

	w = serveAccounts(svc, request(http.MethodGet, "/accounts?includeArchived=true", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, svc.includeArchived)

	w = serveAccounts(svc, request(http.MethodGet, "/accounts?includeArchived=maybe", ""))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	p := problemOf(t, w)
	require.Len(t, p.Errors, 1)
	assert.Equal(t, "includeArchived", p.Errors[0].Field)

	empty := &fakeAccounts{}
	w = serveAccounts(empty, request(http.MethodGet, "/accounts", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"accounts":[]}`, w.Body.String(), "an empty list is [], never null")

	w = serveAccounts(&fakeAccounts{err: errors.New("mongo down")}, request(http.MethodGet, "/accounts", ""))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGetAccount(t *testing.T) {
	svc := &fakeAccounts{account: sampleCard()}
	w := serveAccounts(svc, request(http.MethodGet, "/accounts/a1", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "a1", svc.id)
	assert.Equal(t, "Card", jsonOf(t, w)["name"])

	w = serveAccounts(&fakeAccounts{err: domain.ErrNotFound}, request(http.MethodGet, "/accounts/other", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "not_found", problemOf(t, w).Code)
}

func TestUpdateAccount(t *testing.T) {
	svc := &fakeAccounts{account: sampleCard()}
	w := serveAccounts(svc, request(http.MethodPatch, "/accounts/a1", `{"name":"Travel","archived":true,"card":{"closingDay":5,"dueDay":12}}`))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "a1", svc.id)
	require.NotNil(t, svc.patch.Name)
	assert.Equal(t, "Travel", *svc.patch.Name)
	require.NotNil(t, svc.patch.Archived)
	assert.True(t, *svc.patch.Archived)
	assert.Equal(t, &domain.CardSettings{ClosingDay: 5, DueDay: 12}, svc.patch.Card)
	assert.Nil(t, svc.patch.InitialBalance)

	for name, body := range map[string]string{
		"type never changes":     `{"type":"cash"}`,
		"currency never changes": `{"currency":"USD"}`,
		"source is not editable": `{"source":"open_finance"}`,
		"operator key":           `{"$set":{"user_id":"u2"}}`,
	} {
		svc := &fakeAccounts{account: sampleCard()}
		w := serveAccounts(svc, request(http.MethodPatch, "/accounts/a1", body))
		assert.Equal(t, http.StatusBadRequest, w.Code, name)
		assert.Empty(t, svc.id, name)
	}

	w = serveAccounts(&fakeAccounts{err: domain.ErrNotFound}, request(http.MethodPatch, "/accounts/a9", `{"name":"x"}`))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteAccount(t *testing.T) {
	svc := &fakeAccounts{}
	w := serveAccounts(svc, request(http.MethodDelete, "/accounts/a1", ""))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.True(t, svc.deleted)
	assert.Equal(t, "u1", svc.userID)

	w = serveAccounts(&fakeAccounts{err: domain.ErrNotFound}, request(http.MethodDelete, "/accounts/a1", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAccountsNeedAPrincipal(t *testing.T) {
	h := &Accounts{Service: &fakeAccounts{}, Logger: slog.New(slog.DiscardHandler)}
	for name, fn := range map[string]http.HandlerFunc{
		"list": h.List, "create": h.Create, "get": h.Get, "update": h.Update, "delete": h.Delete,
	} {
		w := serve(fn, request(http.MethodGet, "/accounts", "{}"))
		assert.Equal(t, http.StatusUnauthorized, w.Code, name)
	}
}
