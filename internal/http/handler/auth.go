package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
	"github.com/NickeAlves/monthra-server/internal/http/middleware"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
)

// AuthService is what the auth handlers need from the use case.
type AuthService interface {
	Register(ctx context.Context, in authuc.RegisterInput, c authuc.Client) error
	VerifyEmail(ctx context.Context, token string, c authuc.Client) error
	ResendVerification(ctx context.Context, email string, c authuc.Client) error
	ForgotPassword(ctx context.Context, email string, c authuc.Client) error
	ResetPassword(ctx context.Context, in authuc.ResetPasswordInput, c authuc.Client) error
	Login(ctx context.Context, in authuc.LoginInput, c authuc.Client) (authuc.LoginResult, error)
	VerifyMFA(ctx context.Context, in authuc.VerifyMFAInput, c authuc.Client) (authuc.Tokens, error)
	SetupMFA(ctx context.Context, p auth.Principal, password string, c authuc.Client) (authuc.MFASetup, error)
	EnableMFA(ctx context.Context, p auth.Principal, code string, c authuc.Client) ([]string, error)
	DisableMFA(ctx context.Context, p auth.Principal, in authuc.DisableMFAInput, c authuc.Client) error
	Refresh(ctx context.Context, token string, c authuc.Client) (authuc.Tokens, error)
	Logout(ctx context.Context, p auth.Principal, c authuc.Client) error
}

// Auth serves /api/v1/auth/*.
//
// Browsers (requests with an Origin header or auth cookies) always get the
// tokens as HttpOnly cookies and never in the body, so page scripts cannot
// read them. Native clients get them in the body and send them back as
// Authorization: Bearer (access) or in the refresh body (refresh).
type Auth struct {
	Service AuthService
	Cookies cookie.Policy
	Logger  *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// Register handles POST /auth/register: always 202 for valid input.
func (h *Auth) Register(w http.ResponseWriter, r *http.Request) {
	var req dto.RegisterRequest
	if !h.decode(w, r, &req) {
		return
	}
	in := authuc.RegisterInput{Name: req.Name, Email: req.Email, Password: req.Password, CaptchaToken: req.CaptchaToken}
	if err := h.Service.Register(r.Context(), in, client(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	noContent(w, http.StatusAccepted)
}

// VerifyEmail handles POST /auth/verify-email.
func (h *Auth) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req dto.VerifyEmailRequest
	if !h.decode(w, r, &req) {
		return
	}
	if err := h.Service.VerifyEmail(r.Context(), req.Token, client(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	noContent(w, http.StatusNoContent)
}

// ResendVerification handles POST /auth/verify-email/resend: always 202 for valid input.
func (h *Auth) ResendVerification(w http.ResponseWriter, r *http.Request) {
	var req dto.ResendVerificationRequest
	if !h.decode(w, r, &req) {
		return
	}
	if err := h.Service.ResendVerification(r.Context(), req.Email, client(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	noContent(w, http.StatusAccepted)
}

// ForgotPassword handles POST /auth/forgot-password: always 202 for valid input.
func (h *Auth) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req dto.ForgotPasswordRequest
	if !h.decode(w, r, &req) {
		return
	}
	if err := h.Service.ForgotPassword(r.Context(), req.Email, client(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	noContent(w, http.StatusAccepted)
}

// ResetPassword handles POST /auth/reset-password. The reset signs out every
// session, so a browser also gets its auth cookies cleared.
func (h *Auth) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req dto.ResetPasswordRequest
	if !h.decode(w, r, &req) {
		return
	}
	in := authuc.ResetPasswordInput{Token: req.Token, Password: req.Password}
	if err := h.Service.ResetPassword(r.Context(), in, client(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.isBrowser(r) {
		h.clearCookies(w)
	}
	noContent(w, http.StatusNoContent)
}

// Login handles POST /auth/login.
func (h *Auth) Login(w http.ResponseWriter, r *http.Request) {
	var req dto.LoginRequest
	if !h.decode(w, r, &req) {
		return
	}
	in := authuc.LoginInput{Email: req.Email, Password: req.Password, CaptchaToken: req.CaptchaToken}
	res, err := h.Service.Login(r.Context(), in, client(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if ch := res.Challenge; ch != nil {
		// Not a session yet: the same body for browsers and native clients,
		// and no cookies until the second factor passes.
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, dto.MFAChallengeResponse{MFARequired: true, MFAToken: ch.Token, ExpiresIn: seconds(ch.ExpiresAt.Sub(h.now()))})
		return
	}
	h.deliver(w, r, res.Tokens)
}

// VerifyMFA handles POST /auth/mfa/verify, the second step of a login.
func (h *Auth) VerifyMFA(w http.ResponseWriter, r *http.Request) {
	var req dto.MFAVerifyRequest
	if !h.decode(w, r, &req) {
		return
	}
	tokens, err := h.Service.VerifyMFA(r.Context(), authuc.VerifyMFAInput{Token: req.MFAToken, Code: req.Code}, client(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.deliver(w, r, tokens)
}

// SetupMFA handles POST /auth/mfa/setup (authenticated).
func (h *Auth) SetupMFA(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.MFASetupRequest
	if !h.decode(w, r, &req) {
		return
	}
	setup, err := h.Service.SetupMFA(r.Context(), p, req.Password, client(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, dto.MFASetupResponse{Secret: setup.Secret, OTPAuthURI: setup.URI, ExpiresIn: seconds(setup.ExpiresAt.Sub(h.now()))})
}

// EnableMFA handles POST /auth/mfa/enable (authenticated). The recovery
// codes in the answer are never shown again.
func (h *Auth) EnableMFA(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.MFAEnableRequest
	if !h.decode(w, r, &req) {
		return
	}
	codes, err := h.Service.EnableMFA(r.Context(), p, req.Code, client(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, dto.MFAEnableResponse{RecoveryCodes: codes})
}

// DisableMFA handles POST /auth/mfa/disable (authenticated).
func (h *Auth) DisableMFA(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req dto.MFADisableRequest
	if !h.decode(w, r, &req) {
		return
	}
	if err := h.Service.DisableMFA(r.Context(), p, authuc.DisableMFAInput{Password: req.Password, Code: req.Code}, client(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	noContent(w, http.StatusNoContent)
}

// principal returns the caller set by middleware.Authenticate, or answers 401.
func principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		problem.Write(w, r, http.StatusUnauthorized, i18n.ErrUnauthorized)
	}
	return p, ok
}

// Refresh handles POST /auth/refresh. Browsers send the refresh cookie (its
// Path limits it to this route) and no body; native clients send the token
// in the body.
func (h *Auth) Refresh(w http.ResponseWriter, r *http.Request) {
	var token string
	if h.isBrowser(r) {
		token, _ = h.Cookies.Get(r, cookie.RefreshToken, cookie.RefreshPath)
	} else {
		var req dto.RefreshRequest
		if !h.decode(w, r, &req) {
			return
		}
		token = req.RefreshToken
	}
	tokens, err := h.Service.Refresh(r.Context(), token, client(r))
	if err != nil {
		if errors.Is(err, authuc.ErrUnauthorized) && h.isBrowser(r) {
			h.clearCookies(w)
		}
		h.fail(w, r, err)
		return
	}
	h.deliver(w, r, tokens)
}

// Logout handles POST /auth/logout (authenticated).
func (h *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := h.Service.Logout(r.Context(), p, client(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	h.clearCookies(w)
	noContent(w, http.StatusNoContent)
}

func (h *Auth) deliver(w http.ResponseWriter, r *http.Request, t authuc.Tokens) {
	now := h.now()
	accessFor, refreshFor := t.AccessExpiresAt.Sub(now), t.RefreshExpiresAt.Sub(now)
	resp := dto.SessionResponse{ExpiresIn: seconds(accessFor), RefreshExpiresIn: seconds(refreshFor)}
	if h.isBrowser(r) {
		http.SetCookie(w, h.Cookies.New(cookie.AccessToken, t.AccessToken, cookie.RootPath, true, accessFor))
		http.SetCookie(w, h.Cookies.New(cookie.RefreshToken, t.RefreshToken, cookie.RefreshPath, true, refreshFor))
	} else {
		resp.TokenType, resp.AccessToken, resp.RefreshToken = "Bearer", t.AccessToken, t.RefreshToken
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

func (h *Auth) clearCookies(w http.ResponseWriter) { clearAuthCookies(w, h.Cookies) }

func (h *Auth) isBrowser(r *http.Request) bool { return isBrowser(r, h.Cookies) }

// clearAuthCookies expires the access and refresh cookies.
func clearAuthCookies(w http.ResponseWriter, p cookie.Policy) {
	http.SetCookie(w, p.New(cookie.AccessToken, "", cookie.RootPath, true, -time.Second))
	http.SetCookie(w, p.New(cookie.RefreshToken, "", cookie.RefreshPath, true, -time.Second))
}

// isBrowser reports whether the request comes from a page: it carries an
// Origin header or ambient auth cookies.
func isBrowser(r *http.Request, p cookie.Policy) bool {
	return r.Header.Get("Origin") != "" || p.HasAuth(r)
}

func (h *Auth) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Auth) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeBody(w, r, h.Logger, v)
}

// decodeBody decodes and validates the JSON body into v, or answers the
// problem and returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, logger *slog.Logger, v any) bool {
	if err := dto.Decode(r, v); err != nil {
		writeError(w, r, logger, err)
		return false
	}
	return true
}

// fail maps use case errors to problem responses.
func (h *Auth) fail(w http.ResponseWriter, r *http.Request, err error) {
	writeError(w, r, h.Logger, err)
}

func writeFieldProblem(w http.ResponseWriter, r *http.Request, status int, key i18n.Key, fields []domain.FieldError) {
	p := problem.New(r, status, key)
	for _, f := range fields {
		p.Errors = append(p.Errors, problem.FieldError{Field: f.Field, Code: f.Code})
	}
	problem.WriteProblem(w, r, p)
}

func client(r *http.Request) authuc.Client {
	return authuc.Client{
		IP:        middleware.ClientIPFrom(r.Context()),
		UserAgent: r.UserAgent(),
		Locale:    i18n.FromContext(r.Context()),
	}
}

func noContent(w http.ResponseWriter, status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
}

func seconds(d time.Duration) int { return max(int(d/time.Second), 0) }
