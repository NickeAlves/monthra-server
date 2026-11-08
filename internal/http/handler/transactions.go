package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
	"github.com/NickeAlves/monthra-server/internal/usecase/transactions"
)

// TransactionsService is what the /transactions handlers need from the use
// case.
type TransactionsService interface {
	List(ctx context.Context, userID string, q transactions.Query) (transactions.Page, error)
	Create(ctx context.Context, userID string, in domain.TransactionInput) (domain.Transaction, error)
	Get(ctx context.Context, userID, id string) (domain.Transaction, error)
	Update(ctx context.Context, userID, id string, p domain.TransactionPatch) (domain.Transaction, error)
	Delete(ctx context.Context, userID, id string) error
}

// Transactions serves /api/v1/transactions, always for the caller of the
// access token: the use case scopes every lookup to that user, so another
// user's transaction answers 404 like one that does not exist.
type Transactions struct {
	Service TransactionsService
	Logger  *slog.Logger
}

// Mount adds the routes.
func (h *Transactions) Mount(r chi.Router) {
	r.Get("/transactions", h.List)
	r.Post("/transactions", h.Create)
	r.Get("/transactions/{id}", h.Get)
	r.Patch("/transactions/{id}", h.Update)
	r.Delete("/transactions/{id}", h.Delete)
}

// List handles GET /transactions?from&to&accountId&categoryId&kind&tag&q&limit&cursor.
func (h *Transactions) List(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q, err := dto.ParseTransactionQuery(r.URL.Query())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	page, err := h.Service.List(r.Context(), p.UserID, transactions.Query{Filter: q.Filter, After: q.After, Limit: q.Limit})
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewTransactions(page.Items, page.Next))
}

// Create handles POST /transactions.
func (h *Transactions) Create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.CreateTransactionRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	t, err := h.Service.Create(r.Context(), p.UserID, req.Input())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writeCreatedJSON(w, "/api/v1/transactions/"+t.ID, dto.NewTransaction(t))
}

// Get handles GET /transactions/{id}.
func (h *Transactions) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	t, err := h.Service.Get(r.Context(), p.UserID, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewTransaction(t))
}

// Update handles PATCH /transactions/{id}.
func (h *Transactions) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.UpdateTransactionRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	t, err := h.Service.Update(r.Context(), p.UserID, chi.URLParam(r, "id"), req.Patch())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewTransaction(t))
}

// Delete handles DELETE /transactions/{id}.
func (h *Transactions) Delete(w http.ResponseWriter, r *http.Request) {
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
