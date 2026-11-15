package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
	"github.com/NickeAlves/monthra-server/internal/http/middleware"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/usecase/reports"
)

// ExportWriteTimeout is how long an export may take to send, past the
// server's write timeout (meant for JSON answers).
const ExportWriteTimeout = 5 * time.Minute

// ReportsService is what the /reports handlers need from the use case.
type ReportsService interface {
	Summary(ctx context.Context, userID string, q reports.SummaryQuery) (domain.Report, error)
	ExportTransactions(ctx context.Context, r reports.Requester, q reports.ExportQuery) (reports.TransactionExport, error)
}

// Reports serves /api/v1/reports, always for the caller of the access
// token. Each route has its own budget on top of the per-user one (§6.4):
// SummaryLimit and ExportLimit, which pass every request when nil.
type Reports struct {
	Service      ReportsService
	Logger       *slog.Logger
	SummaryLimit func(http.Handler) http.Handler
	ExportLimit  func(http.Handler) http.Handler
	// Now defaults to time.Now; it names the exported file.
	Now func() time.Time
}

// Mount adds the routes.
func (h *Reports) Mount(r chi.Router) {
	r.With(orPass(h.SummaryLimit)).Get("/reports/summary", h.Summary)
	r.With(orPass(h.ExportLimit)).Get("/reports/export", h.Export)
}

func orPass(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	if mw == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return mw
}

// Summary handles GET /reports/summary?from&to&asOf.
func (h *Reports) Summary(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q, err := dto.ParseReportQuery(r.URL.Query())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	report, err := h.Service.Summary(r.Context(), p.UserID, reports.SummaryQuery{From: q.From, To: q.To, AsOf: q.AsOf})
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewReport(report))
}

// Export handles GET /reports/export?format=csv and the filters of GET
// /transactions: an attachment streamed as the transactions are read. An
// error before the first byte answers a problem; after it the response is
// aborted, so the client sees a failed download instead of a short file.
func (h *Reports) Export(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q, err := dto.ParseExportQuery(r.URL.Query())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	ip := middleware.ClientIPFrom(r.Context())
	requester := reports.Requester{UserID: p.UserID, SessionID: p.SessionID, UserAgent: r.UserAgent()}
	if ip.IsValid() {
		requester.IP = ip.String()
	}
	exp, err := h.Service.ExportTransactions(r.Context(), requester, reports.ExportQuery{Filter: q.Filter, AsOf: q.AsOf})
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(ExportWriteTimeout)); err != nil {
		h.Logger.WarnContext(r.Context(), "extend export write deadline", slog.String("error", err.Error()))
	}
	out := &attachment{w: w, contentType: "text/csv; charset=utf-8", filename: "monthra-transactions-" + h.day(q.AsOf) + ".csv"}
	csv := dto.TransactionsCSV{Locale: i18n.FromContext(r.Context()), Accounts: exp.Accounts, Categories: exp.Categories}
	if err := csv.Write(out, exp.Rows); err != nil {
		if !out.started {
			writeError(w, r, h.Logger, err)
			return
		}
		h.Logger.ErrorContext(r.Context(), "export transactions", slog.String("error", err.Error()))
		panic(http.ErrAbortHandler)
	}
}

// day is the reader's today for the file name: asOf, or today in UTC.
func (h *Reports) day(asOf domain.Date) string {
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	return domain.Today(asOf, now()).String()
}

// attachment sends the headers of a download with its first byte, so a
// failure before it can still answer a problem.
type attachment struct {
	w           http.ResponseWriter
	contentType string
	filename    string
	started     bool
}

func (a *attachment) Write(p []byte) (int, error) {
	if !a.started {
		a.started = true
		h := a.w.Header()
		h.Set("Content-Type", a.contentType)
		h.Set("Content-Disposition", `attachment; filename="`+a.filename+`"`)
		h.Set("Cache-Control", "no-store")
		a.w.WriteHeader(http.StatusOK)
	}
	return a.w.Write(p)
}
