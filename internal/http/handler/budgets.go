package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
)

// BudgetsService is what the /budgets handlers need from the use case.
type BudgetsService interface {
	List(ctx context.Context, userID string, month domain.Month, asOf domain.Date) (domain.BudgetMonth, error)
	Create(ctx context.Context, userID string, in domain.BudgetInput, asOf domain.Date) (domain.BudgetUsage, error)
	Get(ctx context.Context, userID, id string, asOf domain.Date) (domain.BudgetUsage, error)
	Update(ctx context.Context, userID, id string, p domain.BudgetPatch, asOf domain.Date) (domain.BudgetUsage, error)
	Delete(ctx context.Context, userID, id string) error
}

// Budgets serves /api/v1/budgets, always for the caller of the access
// token: the use case scopes every lookup to that user, so another user's
// budget answers 404 like one that does not exist. Each route that reads
// spending takes ?asOf=YYYY-MM-DD, the reader's today, which names their
// month.
type Budgets struct {
	Service BudgetsService
	Logger  *slog.Logger
}

// Mount adds the routes.
func (h *Budgets) Mount(r chi.Router) {
	r.Get("/budgets", h.List)
	r.Post("/budgets", h.Create)
	r.Get("/budgets/{id}", h.Get)
	r.Patch("/budgets/{id}", h.Update)
	r.Delete("/budgets/{id}", h.Delete)
}

// asOf reads ?asOf, answering 422 when it is not a day.
func (h *Budgets) asOf(w http.ResponseWriter, r *http.Request) (domain.Date, bool) {
	d, err := dto.ParseAsOf(r.URL.Query().Get("asOf"))
	if err != nil {
		writeError(w, r, h.Logger, err)
		return domain.Date{}, false
	}
	return d, true
}

// List handles GET /budgets?month=YYYY-MM&asOf: the budgets of the month
// (the reader's by default), whole, a month still to start as a projection.
func (h *Budgets) List(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q, err := dto.ParseMonthQuery(r.URL.Query())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	month, err := h.Service.List(r.Context(), p.UserID, q.Month, q.AsOf)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewBudgets(month))
}

// Create handles POST /budgets?asOf.
func (h *Budgets) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	asOf, ok := h.asOf(w, r)
	if !ok {
		return
	}
	var req dto.CreateBudgetRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	u, err := h.Service.Create(r.Context(), p.UserID, req.Input(), asOf)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writeCreatedJSON(w, "/api/v1/budgets/"+u.Budget.ID, dto.NewBudget(u))
}

// Get handles GET /budgets/{id}?asOf.
func (h *Budgets) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	asOf, ok := h.asOf(w, r)
	if !ok {
		return
	}
	u, err := h.Service.Get(r.Context(), p.UserID, chi.URLParam(r, "id"), asOf)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewBudget(u))
}

// Update handles PATCH /budgets/{id}?asOf.
func (h *Budgets) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	asOf, ok := h.asOf(w, r)
	if !ok {
		return
	}
	var req dto.UpdateBudgetRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	u, err := h.Service.Update(r.Context(), p.UserID, chi.URLParam(r, "id"), req.Patch(), asOf)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewBudget(u))
}

// Delete handles DELETE /budgets/{id}.
func (h *Budgets) Delete(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := h.Service.Delete(r.Context(), p.UserID, chi.URLParam(r, "id")); err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	noContent(w, http.StatusNoContent)
}
