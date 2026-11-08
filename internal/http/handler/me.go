package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
)

// ProfileService is what the /me profile handlers need from the use case.
type ProfileService interface {
	Me(ctx context.Context, userID string) (domain.User, error)
	UpdateName(ctx context.Context, userID, name string) (domain.User, error)
	Preferences(ctx context.Context, userID string) (domain.Preferences, error)
	UpdatePreferences(ctx context.Context, userID string, patch domain.PreferencesPatch) (domain.Preferences, error)
}

// Me serves /api/v1/me/*, always for the caller of the access token: no
// route takes a user id, so one user can never reach another's account.
type Me struct {
	Profile ProfileService
	Cookies cookie.Policy
	Logger  *slog.Logger
}

// Get handles GET /me.
func (h *Me) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	u, err := h.Profile.Me(r.Context(), p.UserID)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewMeResponse(u))
}

// Update handles PATCH /me.
func (h *Me) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.UpdateMeRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	u, err := h.Profile.UpdateName(r.Context(), p.UserID, *req.Name)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewMeResponse(u))
}

// GetPreferences handles GET /me/preferences.
func (h *Me) GetPreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	prefs, err := h.Profile.Preferences(r.Context(), p.UserID)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewPreferences(prefs))
}

// UpdatePreferences handles PATCH /me/preferences.
func (h *Me) UpdatePreferences(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.UpdatePreferencesRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	prefs, err := h.Profile.UpdatePreferences(r.Context(), p.UserID, req.Patch())
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	writePrivateJSON(w, dto.NewPreferences(prefs))
}

// writePrivateJSON answers 200 with personal data, which no cache may keep.
func writePrivateJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}
