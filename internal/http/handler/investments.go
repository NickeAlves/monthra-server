package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
	"github.com/NickeAlves/monthra-server/internal/usecase/investments"
)

// InvestmentsService is what the /investments handlers need from the use
// case.
type InvestmentsService interface {
	List(ctx context.Context, userID string) (domain.Portfolio, error)
	Create(ctx context.Context, userID string, in domain.InvestmentInput) (domain.InvestmentPosition, error)
	Get(ctx context.Context, userID, id string) (domain.InvestmentPosition, error)
	Update(ctx context.Context, userID, id string, p domain.InvestmentPatch) (domain.InvestmentPosition, error)
	Delete(ctx context.Context, userID, id string) error
	ListMovements(ctx context.Context, userID, invID string, q investments.MovementQuery) (investments.MovementPage, error)
	GetMovement(ctx context.Context, userID, invID, id string) (domain.Movement, error)
	CreateMovement(ctx context.Context, userID, invID string, in domain.MovementInput, asOf domain.Date) (domain.Movement, error)
	UpdateMovement(ctx context.Context, userID, invID, id string, p domain.MovementPatch, asOf domain.Date) (domain.Movement, error)
	DeleteMovement(ctx context.Context, userID, invID, id string) error
}

// Investments serves /api/v1/investments and their movements, always for
// the caller of the access token: the use case scopes every lookup to that
// user, so another user's investment (or a movement reached through
// another investment) answers 404 like one that does not exist. The routes
// that write a movement take ?asOf=YYYY-MM-DD, the reader's today, which
// bounds its day.
type Investments struct {
	Service InvestmentsService
	Logger  *slog.Logger
}

// Mount adds the routes.
func (h *Investments) Mount(r chi.Router) {
	r.Get("/investments", h.List)
	r.Post("/investments", h.Create)
	r.Get("/investments/{id}", h.Get)
	r.Patch("/investments/{id}", h.Update)
	r.Delete("/investments/{id}", h.Delete)
	r.Get("/investments/{id}/movements", h.ListMovements)
	r.Post("/investments/{id}/movements", h.CreateMovement)
	r.Get("/investments/{id}/movements/{movementId}", h.GetMovement)
	r.Patch("/investments/{id}/movements/{movementId}", h.UpdateMovement)
	r.Delete("/investments/{id}/movements/{movementId}", h.DeleteMovement)
}

// asOf reads ?asOf, answering 422 when it is not a day.
func (h *Investments) asOf(w http.ResponseWriter, r *http.Request) (domain.Date, bool) {
	d, err := dto.ParseAsOf(r.URL.Query().Get("asOf"))
	if err != nil {
		writeError(w, r, h.Logger, err)
		return domain.Date{}, false
	}
	return d, true
}

// List handles GET /investments: the portfolio, whole.
func (h *Investments) List(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	portfolio, err := h.Service.List(r.Context(), p.UserID)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewInvestments(portfolio))
}

// Create handles POST /investments.
func (h *Investments) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.CreateInvestmentRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	ip, err := h.Service.Create(r.Context(), p.UserID, req.Input())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writeCreatedJSON(w, "/api/v1/investments/"+ip.Investment.ID, dto.NewInvestment(ip))
}

// Get handles GET /investments/{id}.
func (h *Investments) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ip, err := h.Service.Get(r.Context(), p.UserID, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewInvestment(ip))
}

// Update handles PATCH /investments/{id}.
func (h *Investments) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.UpdateInvestmentRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	ip, err := h.Service.Update(r.Context(), p.UserID, chi.URLParam(r, "id"), req.Patch())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewInvestment(ip))
}

// Delete handles DELETE /investments/{id}, with its movements.
func (h *Investments) Delete(w http.ResponseWriter, r *http.Request) {
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

// ListMovements handles GET /investments/{id}/movements?type&limit&cursor.
func (h *Investments) ListMovements(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q, err := dto.ParseMovementQuery(r.URL.Query())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	page, err := h.Service.ListMovements(r.Context(), p.UserID, chi.URLParam(r, "id"),
		investments.MovementQuery{Filter: q.Filter, After: q.After, Limit: q.Limit})
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewMovements(page.Items, page.Next))
}

// CreateMovement handles POST /investments/{id}/movements?asOf.
func (h *Investments) CreateMovement(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	asOf, ok := h.asOf(w, r)
	if !ok {
		return
	}
	var req dto.CreateMovementRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	invID := chi.URLParam(r, "id")
	m, err := h.Service.CreateMovement(r.Context(), p.UserID, invID, req.Input(), asOf)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writeCreatedJSON(w, "/api/v1/investments/"+invID+"/movements/"+m.ID, dto.NewMovement(m))
}

// GetMovement handles GET /investments/{id}/movements/{movementId}.
func (h *Investments) GetMovement(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	m, err := h.Service.GetMovement(r.Context(), p.UserID, chi.URLParam(r, "id"), chi.URLParam(r, "movementId"))
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewMovement(m))
}

// UpdateMovement handles PATCH /investments/{id}/movements/{movementId}?asOf.
func (h *Investments) UpdateMovement(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	asOf, ok := h.asOf(w, r)
	if !ok {
		return
	}
	var req dto.UpdateMovementRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	m, err := h.Service.UpdateMovement(r.Context(), p.UserID, chi.URLParam(r, "id"), chi.URLParam(r, "movementId"), req.Patch(), asOf)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewMovement(m))
}

// DeleteMovement handles DELETE /investments/{id}/movements/{movementId}.
func (h *Investments) DeleteMovement(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := h.Service.DeleteMovement(r.Context(), p.UserID, chi.URLParam(r, "id"), chi.URLParam(r, "movementId")); err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	noContent(w, http.StatusNoContent)
}
