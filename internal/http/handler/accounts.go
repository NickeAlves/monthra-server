package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
)

// AccountsService is what the /accounts handlers need from the use case.
type AccountsService interface {
	List(ctx context.Context, userID string, includeArchived bool) ([]domain.Account, error)
	Create(ctx context.Context, userID string, in domain.AccountInput) (domain.Account, error)
	Get(ctx context.Context, userID, id string) (domain.Account, error)
	Update(ctx context.Context, userID, id string, p domain.AccountPatch) (domain.Account, error)
	Delete(ctx context.Context, userID, id string) error
}

// Accounts serves /api/v1/accounts, always for the caller of the access
// token: the use case scopes every lookup to that user, so another user's
// account answers 404 like one that does not exist.
type Accounts struct {
	Service AccountsService
	Logger  *slog.Logger
}

// Mount adds the routes.
func (h *Accounts) Mount(r chi.Router) {
	r.Get("/accounts", h.List)
	r.Post("/accounts", h.Create)
	r.Get("/accounts/{id}", h.Get)
	r.Patch("/accounts/{id}", h.Update)
	r.Delete("/accounts/{id}", h.Delete)
}

// List handles GET /accounts?includeArchived=true.
func (h *Accounts) List(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	includeArchived := false
	if raw := r.URL.Query().Get("includeArchived"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, r, h.Logger, &domain.ValidationError{Fields: []domain.FieldError{{Field: "includeArchived", Code: domain.CodeInvalid}}})
			return
		}
		includeArchived = v
	}
	list, err := h.Service.List(r.Context(), p.UserID, includeArchived)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewAccounts(list))
}

// Create handles POST /accounts.
func (h *Accounts) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.CreateAccountRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	a, err := h.Service.Create(r.Context(), p.UserID, req.Input())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writeCreatedJSON(w, "/api/v1/accounts/"+a.ID, dto.NewAccount(a))
}

// Get handles GET /accounts/{id}.
func (h *Accounts) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	a, err := h.Service.Get(r.Context(), p.UserID, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewAccount(a))
}

// Update handles PATCH /accounts/{id}.
func (h *Accounts) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.UpdateAccountRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	a, err := h.Service.Update(r.Context(), p.UserID, chi.URLParam(r, "id"), req.Patch())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewAccount(a))
}

// Delete handles DELETE /accounts/{id}.
func (h *Accounts) Delete(w http.ResponseWriter, r *http.Request) {
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
