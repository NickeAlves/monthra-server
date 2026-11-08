package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
)

// RecurrencesService is what the /recurrences handlers need from the use
// case.
type RecurrencesService interface {
	List(ctx context.Context, userID string, asOf domain.Date) ([]domain.Recurrence, error)
	Create(ctx context.Context, userID string, in domain.RecurrenceInput, asOf domain.Date) (domain.Recurrence, error)
	Get(ctx context.Context, userID, id string, asOf domain.Date) (domain.Recurrence, error)
	Update(ctx context.Context, userID, id string, p domain.RecurrencePatch, asOf domain.Date) (domain.Recurrence, error)
	Delete(ctx context.Context, userID, id string) error
	Upcoming(ctx context.Context, userID string, to, asOf domain.Date) ([]domain.Transaction, error)
}

// Recurrences serves /api/v1/recurrences, always for the caller of the
// access token: the use case scopes every lookup to that user, so another
// user's recurrence answers 404 like one that does not exist. Each route
// takes ?asOf=YYYY-MM-DD, the reader's today, up to which the occurrences
// due are created first.
type Recurrences struct {
	Service RecurrencesService
	Logger  *slog.Logger
}

// Mount adds the routes.
func (h *Recurrences) Mount(r chi.Router) {
	r.Get("/recurrences", h.List)
	r.Post("/recurrences", h.Create)
	r.Get("/recurrences/upcoming", h.Upcoming)
	r.Get("/recurrences/{id}", h.Get)
	r.Patch("/recurrences/{id}", h.Update)
	r.Delete("/recurrences/{id}", h.Delete)
}

// asOf reads ?asOf, answering 422 when it is not a day.
func (h *Recurrences) asOf(w http.ResponseWriter, r *http.Request) (domain.Date, bool) {
	d, err := dto.ParseAsOf(r.URL.Query().Get("asOf"))
	if err != nil {
		writeError(w, r, h.Logger, err)
		return domain.Date{}, false
	}
	return d, true
}

// List handles GET /recurrences?asOf.
func (h *Recurrences) List(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	asOf, ok := h.asOf(w, r)
	if !ok {
		return
	}
	list, err := h.Service.List(r.Context(), p.UserID, asOf)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewRecurrences(list))
}

// Create handles POST /recurrences?asOf; a start in the past creates the
// occurrences due up to today.
func (h *Recurrences) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	asOf, ok := h.asOf(w, r)
	if !ok {
		return
	}
	var req dto.CreateRecurrenceRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	rec, err := h.Service.Create(r.Context(), p.UserID, req.Input(), asOf)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writeCreatedJSON(w, "/api/v1/recurrences/"+rec.ID, dto.NewRecurrence(rec))
}

// Get handles GET /recurrences/{id}?asOf.
func (h *Recurrences) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	asOf, ok := h.asOf(w, r)
	if !ok {
		return
	}
	rec, err := h.Service.Get(r.Context(), p.UserID, chi.URLParam(r, "id"), asOf)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewRecurrence(rec))
}

// Update handles PATCH /recurrences/{id}?asOf.
func (h *Recurrences) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	asOf, ok := h.asOf(w, r)
	if !ok {
		return
	}
	var req dto.UpdateRecurrenceRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	rec, err := h.Service.Update(r.Context(), p.UserID, chi.URLParam(r, "id"), req.Patch(), asOf)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewRecurrence(rec))
}

// Delete handles DELETE /recurrences/{id}; the transactions it created stay.
func (h *Recurrences) Delete(w http.ResponseWriter, r *http.Request) {
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

// Upcoming handles GET /recurrences/upcoming?to&asOf: the occurrences not
// created yet, up to day to.
func (h *Recurrences) Upcoming(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	to, asOf, err := dto.ParseUpcomingQuery(r.URL.Query())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	list, err := h.Service.Upcoming(r.Context(), p.UserID, to, asOf)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewUpcoming(list))
}
