package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// CategoriesService is what the /categories handlers need from the use case.
type CategoriesService interface {
	List(ctx context.Context, userID string, kind domain.CategoryKind) ([]domain.Category, error)
	Create(ctx context.Context, userID string, in domain.CategoryInput) (domain.Category, error)
	Get(ctx context.Context, userID, id string) (domain.Category, error)
	Update(ctx context.Context, userID, id string, p domain.CategoryPatch) (domain.Category, error)
	Delete(ctx context.Context, userID, id, reassignTo string) error
}

// Categories serves /api/v1/categories, always for the caller of the access
// token: the use case scopes every lookup to that user, so another user's
// category answers 404 like one that does not exist. Default categories are
// named in the language of the request (the stored preference), until
// their owner renames them.
type Categories struct {
	Service CategoriesService
	Logger  *slog.Logger
}

// Mount adds the routes.
func (h *Categories) Mount(r chi.Router) {
	r.Get("/categories", h.List)
	r.Post("/categories", h.Create)
	r.Get("/categories/{id}", h.Get)
	r.Patch("/categories/{id}", h.Update)
	r.Delete("/categories/{id}", h.Delete)
}

// List handles GET /categories?kind=expense|income.
func (h *Categories) List(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var kind domain.CategoryKind
	if raw := r.URL.Query().Get("kind"); raw != "" {
		k, ok := domain.ParseCategoryKind(raw)
		if !ok {
			writeError(w, r, h.Logger, &domain.ValidationError{Fields: []domain.FieldError{{Field: "kind", Code: domain.CodeInvalid}}})
			return
		}
		kind = k
	}
	list, err := h.Service.List(r.Context(), p.UserID, kind)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewCategories(list, i18n.FromContext(r.Context())))
}

// Create handles POST /categories.
func (h *Categories) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.CreateCategoryRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	c, err := h.Service.Create(r.Context(), p.UserID, req.Input())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writeCreatedJSON(w, "/api/v1/categories/"+c.ID, dto.NewCategory(c, i18n.FromContext(r.Context())))
}

// Get handles GET /categories/{id}.
func (h *Categories) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	c, err := h.Service.Get(r.Context(), p.UserID, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewCategory(c, i18n.FromContext(r.Context())))
}

// Update handles PATCH /categories/{id}.
func (h *Categories) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.UpdateCategoryRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	c, err := h.Service.Update(r.Context(), p.UserID, chi.URLParam(r, "id"), req.Patch())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewCategory(c, i18n.FromContext(r.Context())))
}

// Delete handles DELETE /categories/{id}?reassignTo={id}. A category with
// transactions answers 409 category_in_use unless reassignTo names another
// category of the same kind to move them to.
func (h *Categories) Delete(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	reassignTo := r.URL.Query().Get("reassignTo")
	if err := h.Service.Delete(r.Context(), p.UserID, chi.URLParam(r, "id"), reassignTo); err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	noContent(w, http.StatusNoContent)
}
