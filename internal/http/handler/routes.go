package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes is a feature (accounts, categories, ...) that adds its endpoints to
// the authenticated group of /api/v1, so every one of them runs behind
// Authenticate, the per-user budget and the stored locale.
type Routes interface {
	Mount(r chi.Router)
}

// writeCreatedJSON answers 201 with the new record and where it lives.
func writeCreatedJSON(w http.ResponseWriter, location string, v any) {
	w.Header().Set("Location", location)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, v)
}
