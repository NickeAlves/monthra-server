package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
)

// DashboardService is what the /dashboard handlers need from the use case.
type DashboardService interface {
	Summary(ctx context.Context, userID string, month domain.Month, asOf domain.Date) (domain.Summary, error)
}

// Dashboard serves /api/v1/dashboard, always for the caller of the access
// token.
type Dashboard struct {
	Service DashboardService
	Logger  *slog.Logger
}

// Mount adds the routes.
func (h *Dashboard) Mount(r chi.Router) {
	r.Get("/dashboard/summary", h.Summary)
}

// Summary handles GET /dashboard/summary?month=YYYY-MM&asOf=YYYY-MM-DD:
// the caller's month (the reader's by default) by currency. asOf is the
// reader's local today.
func (h *Dashboard) Summary(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q, err := dto.ParseMonthQuery(r.URL.Query())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	sum, err := h.Service.Summary(r.Context(), p.UserID, q.Month, q.AsOf)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewSummary(sum))
}
