package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
	"github.com/NickeAlves/monthra-server/internal/usecase/transactions"
)

type fakeTransactions struct {
	tx         domain.Transaction
	page       transactions.Page
	err        error
	userID, id string
	query      transactions.Query
	input      domain.TransactionInput
	patch      domain.TransactionPatch
	scope      domain.EditScope
	deleted    bool
}

func (f *fakeTransactions) List(_ context.Context, userID string, q transactions.Query) (transactions.Page, error) {
	f.userID, f.query = userID, q
	return f.page, f.err
}

func (f *fakeTransactions) Create(_ context.Context, userID string, in domain.TransactionInput) (domain.Transaction, error) {
	f.userID, f.input = userID, in
	return f.tx, f.err
}

func (f *fakeTransactions) Get(_ context.Context, userID, id string) (domain.Transaction, error) {
	f.userID, f.id = userID, id
	return f.tx, f.err
}

func (f *fakeTransactions) Update(_ context.Context, userID, id string, p domain.TransactionPatch, scope domain.EditScope) (domain.Transaction, error) {
	f.userID, f.id, f.patch, f.scope = userID, id, p, scope
	return f.tx, f.err
}

func (f *fakeTransactions) Delete(_ context.Context, userID, id string, scope domain.EditScope) error {
	f.userID, f.id, f.scope, f.deleted = userID, id, scope, f.err == nil
	return f.err
}

func day(t *testing.T, s string) domain.Date {
	t.Helper()
	d, err := domain.ParseDate(s)
	require.NoError(t, err)
	return d
}

func sampleTransfer(t *testing.T) domain.Transaction {
	return domain.Transaction{
		ID: "t1", UserID: "u1", Kind: domain.TransactionTransfer, AccountID: "a1", Currency: "BRL",
		ToAccountID: "a2", ToCurrency: "USD", Amount: 100000, ToAmount: 19000, Date: day(t, "2026-11-08"),
		Description: "Trip money", Tags: []string{"travel"}, Source: domain.SourceManual, CreatedAt: t0, UpdatedAt: t0,
	}
}

// serveTransactions routes r through the transactions routes as user u1.
func serveTransactions(svc *fakeTransactions, r *http.Request) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	(&Transactions{Service: svc, Logger: slog.New(slog.DiscardHandler)}).Mount(router)
	asUser(r)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestCreateTransaction(t *testing.T) {
	svc := &fakeTransactions{tx: sampleTransfer(t)}
	body := `{"kind":"transfer","accountId":"a1","toAccountId":"a2","amount":100000,"toAmount":19000,"date":"2026-11-08","description":"Trip money","tags":["travel"]}`
	w := serveTransactions(svc, request(http.MethodPost, "/transactions", body))

	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "/api/v1/transactions/t1", w.Header().Get("Location"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "u1", svc.userID, "the owner comes from the token")
	toAmount := int64(19000)
	assert.Equal(t, domain.TransactionInput{
		Kind: "transfer", AccountID: "a1", ToAccountID: "a2", Amount: 100000, ToAmount: &toAmount,
		Date: "2026-11-08", Description: "Trip money", Tags: []string{"travel"},
	}, svc.input)
	assert.Equal(t, map[string]any{
		"id": "t1", "kind": "transfer", "accountId": "a1", "currency": "BRL", "categoryId": nil,
		"toAccountId": "a2", "toCurrency": "USD", "amount": float64(100000), "toAmount": float64(19000),
		"date": "2026-11-08", "invoiceMonth": nil, "toInvoiceMonth": nil, "description": "Trip money",
		"tags": []any{"travel"}, "installment": nil, "recurrence": nil, "source": "manual", "createdAt": "2026-11-08T12:00:00Z", "updatedAt": "2026-11-08T12:00:00Z",
	}, jsonOf(t, w), "no owner id and no search field in the answer")
}

func TestTransactionInvoices(t *testing.T) {
	dec, err := domain.ParseMonth("2026-12")
	require.NoError(t, err)
	tx := sampleTransfer(t)
	tx.InvoiceMonth, tx.ToInvoiceMonth = dec, dec.AddMonths(1)
	svc := &fakeTransactions{tx: tx}
	body := `{"kind":"transfer","accountId":"a1","toAccountId":"a2","amount":1,"date":"2026-11-08","invoiceMonth":"2026-12","toInvoiceMonth":"2027-01"}`
	w := serveTransactions(svc, request(http.MethodPost, "/transactions", body))
	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "2026-12", svc.input.InvoiceMonth)
	assert.Equal(t, "2027-01", svc.input.ToInvoiceMonth)
	got := jsonOf(t, w)
	assert.Equal(t, "2026-12", got["invoiceMonth"])
	assert.Equal(t, "2027-01", got["toInvoiceMonth"])

	w = serveTransactions(svc, request(http.MethodPatch, "/transactions/t1", `{"invoiceMonth":"","toInvoiceMonth":"2027-02"}`))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, svc.patch.InvoiceMonth)
	assert.Empty(t, *svc.patch.InvoiceMonth, "empty picks the invoice from the day again")
	require.NotNil(t, svc.patch.ToInvoiceMonth)
	assert.Equal(t, "2027-02", *svc.patch.ToInvoiceMonth)
}

func TestTransactionInstallments(t *testing.T) {
	tx := sampleTransfer(t)
	tx.Kind, tx.ToAccountID, tx.ToCurrency, tx.ToAmount = domain.TransactionExpense, "", "", 0
	tx.Installment = &domain.Installment{GroupID: "g1", Number: 1, Count: 12}
	svc := &fakeTransactions{tx: tx}
	body := `{"kind":"expense","accountId":"a1","categoryId":"c1","amount":120000,"date":"2026-11-08","installments":12}`
	w := serveTransactions(svc, request(http.MethodPost, "/transactions", body))
	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, 12, svc.input.Installments)
	assert.Equal(t, "/api/v1/transactions/t1", w.Header().Get("Location"), "the first installment")
	assert.Equal(t, map[string]any{"groupId": "g1", "number": float64(1), "count": float64(12)}, jsonOf(t, w)["installment"])

	for query, want := range map[string]domain.EditScope{"": domain.ScopeThis, "?scope=this": domain.ScopeThis, "?scope=future": domain.ScopeFuture, "?scope=all": domain.ScopeAll} {
		svc := &fakeTransactions{tx: tx}
		w := serveTransactions(svc, request(http.MethodPatch, "/transactions/t1"+query, `{"description":"Sofa"}`))
		require.Equal(t, http.StatusOK, w.Code, query)
		assert.Equal(t, want, svc.scope, query)

		w = serveTransactions(svc, request(http.MethodDelete, "/transactions/t1"+query, ""))
		require.Equal(t, http.StatusNoContent, w.Code, query)
		assert.Equal(t, want, svc.scope, query)
	}

	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		svc := &fakeTransactions{tx: tx}
		w := serveTransactions(svc, request(method, "/transactions/t1?scope=everything", `{"description":"Sofa"}`))
		require.Equal(t, http.StatusUnprocessableEntity, w.Code, method)
		errs := problemOf(t, w).Errors
		require.Len(t, errs, 1)
		assert.Equal(t, "scope", errs[0].Field)
		assert.Equal(t, domain.CodeInvalid, errs[0].Code)
		assert.Empty(t, svc.userID, "%s: the use case is never reached", method)
	}

	svc = &fakeTransactions{}
	w = serveTransactions(svc, request(http.MethodGet, "/transactions?installmentGroupId=g1", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "g1", svc.query.Filter.InstallmentGroupID)
}

func TestTransactionOccurrences(t *testing.T) {
	tx := sampleTransfer(t)
	tx.Recurrence = &domain.RecurrenceLink{ID: "r1", Date: day(t, "2026-11-05")}
	svc := &fakeTransactions{tx: tx}
	w := serveTransactions(svc, request(http.MethodGet, "/transactions/t1", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, map[string]any{"id": "r1", "occurrenceDate": "2026-11-05"}, jsonOf(t, w)["recurrence"])

	w = serveTransactions(svc, request(http.MethodGet, "/transactions?recurrenceId=r1&asOf=2026-11-09", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "r1", svc.query.Filter.RecurrenceID)
	assert.Equal(t, "2026-11-09", svc.query.AsOf.String(), "the occurrences are created up to the reader's today")

	svc = &fakeTransactions{}
	w = serveTransactions(svc, request(http.MethodGet, "/transactions?asOf=tomorrow", ""))
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, "asOf", problemOf(t, w).Errors[0].Field)
	assert.Empty(t, svc.userID)
}

func TestTransactionViewOfAnExpense(t *testing.T) {
	svc := &fakeTransactions{tx: domain.Transaction{
		ID: "t2", Kind: domain.TransactionExpense, AccountID: "a1", Currency: "BRL", CategoryID: "c1",
		Amount: 4590, Date: day(t, "2026-11-01"), Source: domain.SourceManual, CreatedAt: t0, UpdatedAt: t0,
	}}
	w := serveTransactions(svc, request(http.MethodGet, "/transactions/t2", ""))
	require.Equal(t, http.StatusOK, w.Code)
	body := jsonOf(t, w)
	assert.Equal(t, "c1", body["categoryId"])
	assert.Nil(t, body["toAccountId"])
	assert.Nil(t, body["toCurrency"])
	assert.Nil(t, body["toAmount"])
	assert.Equal(t, []any{}, body["tags"], "never null")
	assert.Equal(t, "t2", svc.id)
}

func TestCreateTransactionRejectsUnsafeBodies(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"owner in the body":  {`{"kind":"expense","userId":"u2"}`, http.StatusBadRequest, "bad_request"},
		"source in the body": {`{"kind":"expense","source":"open_finance"}`, http.StatusBadRequest, "bad_request"},
		"operator key":       {`{"kind":"expense","$set":{"user_id":"u2"}}`, http.StatusBadRequest, "bad_request"},
		"amount as a float":  {`{"kind":"expense","amount":45.9}`, http.StatusUnprocessableEntity, "validation_failed"},
		"amount as a string": {`{"kind":"expense","amount":"4590"}`, http.StatusUnprocessableEntity, "validation_failed"},
		"tags not a list":    {`{"kind":"expense","tags":"a"}`, http.StatusUnprocessableEntity, "validation_failed"},
	} {
		svc := &fakeTransactions{}
		w := serveTransactions(svc, request(http.MethodPost, "/transactions", tc.body))
		assert.Equal(t, tc.status, w.Code, name)
		assert.Equal(t, tc.code, problemOf(t, w).Code, name)
		assert.Empty(t, svc.userID, "%s: the use case is never reached", name)
	}
}

func TestTransactionErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"invalid":   {&domain.ValidationError{Fields: []domain.FieldError{{Field: "accountId", Code: domain.CodeArchived}}}, http.StatusUnprocessableEntity, "validation_failed"},
		"not found": {domain.ErrNotFound, http.StatusNotFound, "not_found"},
		"down":      {errors.New("mongo down"), http.StatusInternalServerError, "internal_error"},
	} {
		w := serveTransactions(&fakeTransactions{err: tc.err}, request(http.MethodPost, "/transactions", `{"kind":"expense"}`))
		assert.Equal(t, tc.status, w.Code, name)
		assert.Equal(t, tc.code, problemOf(t, w).Code, name)
	}
}

func TestListTransactions(t *testing.T) {
	tx := sampleTransfer(t)
	next := &domain.TransactionCursor{Date: tx.Date, ID: tx.ID}
	svc := &fakeTransactions{page: transactions.Page{Items: []domain.Transaction{tx}, Next: next}}
	cursor := dto.EncodeCursor(domain.TransactionCursor{Date: day(t, "2026-11-09"), ID: "abc123"})
	w := serveTransactions(svc, request(http.MethodGet,
		"/transactions?from=2026-11-01&to=2026-11-30&accountId=a1&categoryId=c1&kind=transfer&tag=%20Travel&q=Caf%C3%A9&invoice=2026-12&limit=20&cursor="+cursor, ""))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, transactions.Query{
		Filter: domain.TransactionFilter{
			From: day(t, "2026-11-01"), To: day(t, "2026-11-30"), AccountID: "a1", CategoryID: "c1",
			Kind: domain.TransactionTransfer, Tag: "travel", Search: "cafe", Invoice: domain.NewMonth(2026, 12),
		},
		After: &domain.TransactionCursor{Date: day(t, "2026-11-09"), ID: "abc123"},
		Limit: 20,
	}, svc.query)
	body := jsonOf(t, w)
	list, _ := body["transactions"].([]any)
	assert.Len(t, list, 1)
	assert.Equal(t, dto.EncodeCursor(*next), body["nextCursor"])

	svc = &fakeTransactions{}
	w = serveTransactions(svc, request(http.MethodGet, "/transactions?tag=&q=", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, transactions.Query{}, svc.query, "empty parameters are ignored")
	assert.JSONEq(t, `{"transactions":[],"nextCursor":null}`, w.Body.String())
}

func TestListTransactionsRejectsBadQueries(t *testing.T) {
	for query, want := range map[string]map[string]string{
		"from=2026-13-01":                {"from": domain.CodeInvalid},
		"to=1800-01-01":                  {"to": domain.CodeOutOfRange},
		"from=2026-11-10&to=2026-11-09":  {"to": domain.CodeInvalid},
		"kind=refund":                    {"kind": domain.CodeInvalid},
		"limit=ten":                      {"limit": domain.CodeInvalid},
		"cursor=!!!":                     {"cursor": domain.CodeInvalid},
		"cursor=" + b64("2026-11-08"):    {"cursor": domain.CodeInvalid},
		"cursor=" + b64("nope|abc"):      {"cursor": domain.CodeInvalid},
		"cursor=" + b64("2026-11-08|$x"): {"cursor": domain.CodeInvalid},
		"q=" + longText(101):             {"q": domain.CodeTooLong},
		"tag=" + longText(31):            {"tag": domain.CodeTooLong},
		"kind=x&limit=y":                 {"kind": domain.CodeInvalid, "limit": domain.CodeInvalid},
		"accountId=a1&invoice=2026-1":    {"invoice": domain.CodeInvalid},
		"accountId=a1&invoice=2101-01":   {"invoice": domain.CodeOutOfRange},
		"invoice=2026-12":                {"accountId": domain.CodeRequired},
	} {
		svc := &fakeTransactions{}
		w := serveTransactions(svc, request(http.MethodGet, "/transactions?"+query, ""))
		require.Equal(t, http.StatusUnprocessableEntity, w.Code, query)
		got := map[string]string{}
		for _, f := range problemOf(t, w).Errors {
			got[f.Field] = f.Code
		}
		assert.Equal(t, want, got, query)
		assert.Empty(t, svc.userID, "%s: the use case is never reached", query)
	}

	svc := &fakeTransactions{err: &domain.ValidationError{Fields: []domain.FieldError{{Field: "limit", Code: domain.CodeOutOfRange}}}}
	w := serveTransactions(svc, request(http.MethodGet, "/transactions?limit=101", ""))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "the use case bounds the page size")
	assert.Equal(t, 101, svc.query.Limit)
}

func TestUpdateTransaction(t *testing.T) {
	svc := &fakeTransactions{tx: sampleTransfer(t)}
	w := serveTransactions(svc, request(http.MethodPatch, "/transactions/t1", `{"amount":5000,"description":"","tags":[],"categoryId":null}`))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "t1", svc.id)
	require.NotNil(t, svc.patch.Amount)
	assert.Equal(t, int64(5000), *svc.patch.Amount)
	require.NotNil(t, svc.patch.Description)
	assert.Empty(t, *svc.patch.Description, "an empty description clears it")
	require.NotNil(t, svc.patch.Tags)
	assert.Empty(t, *svc.patch.Tags)
	assert.Nil(t, svc.patch.CategoryID, "null leaves it as it is")
	assert.Nil(t, svc.patch.AccountID)

	for name, body := range map[string]string{
		"kind never changes":      `{"kind":"income"}`,
		"currency is not a field": `{"currency":"USD"}`,
		"operator key":            `{"$inc":{"amount":1}}`,
	} {
		svc := &fakeTransactions{tx: sampleTransfer(t)}
		w := serveTransactions(svc, request(http.MethodPatch, "/transactions/t1", body))
		assert.Equal(t, http.StatusBadRequest, w.Code, name)
		assert.Empty(t, svc.id, name)
	}

	w = serveTransactions(&fakeTransactions{err: domain.ErrNotFound}, request(http.MethodPatch, "/transactions/t9", `{"amount":1}`))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteTransaction(t *testing.T) {
	svc := &fakeTransactions{}
	w := serveTransactions(svc, request(http.MethodDelete, "/transactions/t1", ""))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.True(t, svc.deleted)
	assert.Equal(t, "u1", svc.userID)

	w = serveTransactions(&fakeTransactions{err: domain.ErrNotFound}, request(http.MethodDelete, "/transactions/t1", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
	w = serveTransactions(&fakeTransactions{err: errors.New("down")}, request(http.MethodGet, "/transactions/t1", ""))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	w = serveTransactions(&fakeTransactions{err: errors.New("down")}, request(http.MethodGet, "/transactions", ""))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestTransactionsNeedAPrincipal(t *testing.T) {
	h := &Transactions{Service: &fakeTransactions{}, Logger: slog.New(slog.DiscardHandler)}
	for name, fn := range map[string]http.HandlerFunc{
		"list": h.List, "create": h.Create, "get": h.Get, "update": h.Update, "delete": h.Delete,
	} {
		w := serve(fn, request(http.MethodGet, "/transactions", "{}"))
		assert.Equal(t, http.StatusUnauthorized, w.Code, name)
	}
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func longText(n int) string { return strings.Repeat("a", n) }
