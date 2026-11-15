package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/usecase/reports"
)

type fakeReports struct {
	report    domain.Report
	export    reports.TransactionExport
	err       error
	userID    string
	summary   reports.SummaryQuery
	requester reports.Requester
	exportQ   reports.ExportQuery
}

func (f *fakeReports) Summary(_ context.Context, userID string, q reports.SummaryQuery) (domain.Report, error) {
	f.userID, f.summary = userID, q
	return f.report, f.err
}

func (f *fakeReports) ExportTransactions(_ context.Context, r reports.Requester, q reports.ExportQuery) (reports.TransactionExport, error) {
	f.requester, f.exportQ = r, q
	return f.export, f.err
}

// serveReports routes r through the reports routes as user u1.
func serveReports(h *Reports, r *http.Request) *httptest.ResponseRecorder {
	if h.Logger == nil {
		h.Logger = slog.New(slog.DiscardHandler)
	}
	if h.Now == nil {
		h.Now = func() time.Time { return t0 }
	}
	router := chi.NewRouter()
	h.Mount(router)
	asUser(r)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestReportsSummary(t *testing.T) {
	from, err := domain.ParseMonth("2026-10")
	require.NoError(t, err)
	to := from.AddMonths(1)
	svc := &fakeReports{report: domain.Report{
		Period: domain.Period{From: from, To: to}, Previous: domain.Period{From: from.AddMonths(-2), To: from.AddMonths(-1)},
		AsOf: day(t, "2026-11-08"),
	}}
	w := serveReports(&Reports{Service: svc}, request(http.MethodGet, "/reports/summary?from=2026-10&to=2026-11&asOf=2026-11-08", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "u1", svc.userID)
	assert.Equal(t, reports.SummaryQuery{From: from, To: to, AsOf: day(t, "2026-11-08")}, svc.summary)
	assert.Equal(t, map[string]any{
		"from": "2026-10", "to": "2026-11", "asOf": "2026-11-08",
		"previous": map[string]any{"from": "2026-08", "to": "2026-09"}, "currencies": []any{},
	}, jsonOf(t, w))

	w = serveReports(&Reports{Service: &fakeReports{}}, request(http.MethodGet, "/reports/summary?from=2026-13", ""))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), `"field":"from"`)

	svc = &fakeReports{err: &domain.ValidationError{Fields: []domain.FieldError{{Field: "to", Code: domain.CodeOutOfRange}}}}
	w = serveReports(&Reports{Service: svc}, request(http.MethodGet, "/reports/summary?from=2020-01", ""))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"out_of_range"`)
}

func TestReportsLimits(t *testing.T) {
	var limited []string
	limit := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				limited = append(limited, name)
				w.WriteHeader(http.StatusTooManyRequests)
			})
		}
	}
	h := &Reports{Service: &fakeReports{}, SummaryLimit: limit("summary"), ExportLimit: limit("export")}
	assert.Equal(t, http.StatusTooManyRequests, serveReports(h, request(http.MethodGet, "/reports/summary", "")).Code)
	assert.Equal(t, http.StatusTooManyRequests, serveReports(h, request(http.MethodGet, "/reports/export?format=csv", "")).Code)
	assert.Equal(t, []string{"summary", "export"}, limited, "each route has its own budget")
}

func exportOf(rows []domain.Transaction, err error) reports.TransactionExport {
	return reports.TransactionExport{
		Accounts:   map[string]domain.Account{"a1": {ID: "a1", Name: "Bank"}},
		Categories: map[string]domain.Category{"c1": {ID: "c1", Name: "Food"}},
		Rows: func(yield func(domain.Transaction, error) bool) {
			for _, t := range rows {
				if !yield(t, nil) {
					return
				}
			}
			if err != nil {
				yield(domain.Transaction{}, err)
			}
		},
	}
}

func TestReportsExport(t *testing.T) {
	tx := domain.Transaction{Kind: domain.TransactionExpense, AccountID: "a1", Currency: "BRL", CategoryID: "c1", Amount: 1250, Date: day(t, "2026-11-08")}
	svc := &fakeReports{export: exportOf([]domain.Transaction{tx}, nil)}
	r := request(http.MethodGet, "/reports/export?format=csv&tag=home&asOf=2026-11-07", "")
	r.Header.Set("User-Agent", "curl/8")
	w := serveReports(&Reports{Service: svc}, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/csv; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="monthra-transactions-2026-11-07.csv"`, w.Header().Get("Content-Disposition"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "\ufeffDate,Type,Description,Category,Account,Amount,Currency,Destination account,Destination amount,Destination currency,Tags,Installment,Card invoice\r\n"+
		"2026-11-08,Expense,,Food,Bank,-12.50,BRL,,,,,,\r\n", w.Body.String(), "in the caller's locale")
	assert.Equal(t, reports.Requester{UserID: "u1", SessionID: "s1", UserAgent: "curl/8"}, svc.requester)
	assert.Equal(t, reports.ExportQuery{Filter: domain.TransactionFilter{Tag: "home"}, AsOf: day(t, "2026-11-07")}, svc.exportQ)

	// Without asOf, the file is named after today in UTC.
	svc = &fakeReports{export: exportOf(nil, nil)}
	w = serveReports(&Reports{Service: svc}, request(http.MethodGet, "/reports/export?format=csv", "", func(r *http.Request) {
		*r = *r.WithContext(i18n.WithLocale(r.Context(), i18n.PtBR))
	}))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, `attachment; filename="monthra-transactions-2026-11-08.csv"`, w.Header().Get("Content-Disposition"))
	assert.True(t, strings.HasPrefix(w.Body.String(), "\ufeffData;Tipo;"), "a header alone, in pt-BR")
}

func TestReportsExportErrors(t *testing.T) {
	w := serveReports(&Reports{Service: &fakeReports{}}, request(http.MethodGet, "/reports/export?format=pdf", ""))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), `"field":"format"`)

	w = serveReports(&Reports{Service: &fakeReports{err: errors.New("boom")}}, request(http.MethodGet, "/reports/export?format=csv", ""))
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	// A first read that fails answers a problem, without the download's headers.
	svc := &fakeReports{export: exportOf(nil, errors.New("boom"))}
	w = serveReports(&Reports{Service: svc}, request(http.MethodGet, "/reports/export?format=csv", ""))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
	assert.Empty(t, w.Header().Get("Content-Disposition"))

	// Once the file started, a failure aborts the response.
	rows := make([]domain.Transaction, 500)
	for i := range rows {
		rows[i] = domain.Transaction{Kind: domain.TransactionIncome, Description: strings.Repeat("x", 100), Date: day(t, "2026-11-08")}
	}
	svc = &fakeReports{export: exportOf(rows, errors.New("boom"))}
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		serveReports(&Reports{Service: svc}, request(http.MethodGet, "/reports/export?format=csv", ""))
	})
}
