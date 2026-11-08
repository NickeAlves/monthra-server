package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
)

// ProfileService is what the /me profile handlers need from the use case.
type ProfileService interface {
	Me(ctx context.Context, userID string) (domain.User, error)
	UpdateName(ctx context.Context, userID, name string) (domain.User, error)
	Preferences(ctx context.Context, userID string) (domain.Preferences, error)
	UpdatePreferences(ctx context.Context, userID string, patch domain.PreferencesPatch) (domain.Preferences, error)
}

// AccountService is what the /me credential handlers need from the use case.
type AccountService interface {
	ChangePassword(ctx context.Context, p auth.Principal, in authuc.ChangePasswordInput, c authuc.Client) error
	ListSessions(ctx context.Context, p auth.Principal) ([]authuc.SessionInfo, error)
	RevokeSession(ctx context.Context, p auth.Principal, sessionID string, c authuc.Client) (current bool, err error)
	RevokeOtherSessions(ctx context.Context, p auth.Principal, c authuc.Client) error
	DeleteAccount(ctx context.Context, p auth.Principal, in authuc.DeleteAccountInput, c authuc.Client) error
}

// Me serves /api/v1/me/*, always for the caller of the access token: no
// route takes a user id, so one user can never reach another's account.
type Me struct {
	Profile ProfileService
	Account AccountService
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

// ChangePassword handles POST /me/password. The other sessions are signed
// out; the caller's stays.
func (h *Me) ChangePassword(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.ChangePasswordRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	in := authuc.ChangePasswordInput{CurrentPassword: req.CurrentPassword, NewPassword: req.NewPassword}
	if err := h.Account.ChangePassword(r.Context(), p, in, client(r)); err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	noContent(w, http.StatusNoContent)
}

// ListSessions handles GET /me/sessions.
func (h *Me) ListSessions(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	sessions, err := h.Account.ListSessions(r.Context(), p)
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	resp := dto.SessionsResponse{Sessions: make([]dto.SessionView, len(sessions))}
	for i, s := range sessions {
		resp.Sessions[i] = dto.SessionView{
			ID: s.ID, CreatedAt: s.CreatedAt.UTC(), LastUsedAt: s.LastUsedAt.UTC(), ExpiresAt: s.ExpiresAt.UTC(),
			UserAgent: s.UserAgent, IP: s.IP, Current: s.Current,
		}
	}
	writePrivateJSON(w, resp)
}

// RevokeSession handles DELETE /me/sessions/{id}. Revoking the caller's own
// session signs it out, so a browser also gets its cookies cleared.
func (h *Me) RevokeSession(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	current, err := h.Account.RevokeSession(r.Context(), p, chi.URLParam(r, "id"), client(r))
	if err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	if current && isBrowser(r, h.Cookies) {
		clearAuthCookies(w, h.Cookies)
	}
	noContent(w, http.StatusNoContent)
}

// RevokeOtherSessions handles DELETE /me/sessions: every session but the caller's.
func (h *Me) RevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := h.Account.RevokeOtherSessions(r.Context(), p, client(r)); err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	noContent(w, http.StatusNoContent)
}

// Delete handles DELETE /me: the account and its data are erased (LGPD).
func (h *Me) Delete(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.DeleteAccountRequest
	if !decodeBody(w, r, h.Logger, &req) {
		return
	}
	if err := h.Account.DeleteAccount(r.Context(), p, authuc.DeleteAccountInput{Password: req.Password, Code: req.Code}, client(r)); err != nil {
		writeError(w, r, h.Logger, err)
		return
	}
	clearAuthCookies(w, h.Cookies)
	noContent(w, http.StatusNoContent)
}

// writePrivateJSON answers 200 with personal data, which no cache may keep.
func writePrivateJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}
