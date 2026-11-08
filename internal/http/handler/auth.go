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
	Login(ctx context.Context, in authuc.LoginInput, c authuc.Client) (authuc.Tokens, error)
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

// Login handles POST /auth/login.
func (h *Auth) Login(w http.ResponseWriter, r *http.Request) {
	var req dto.LoginRequest
	if !h.decode(w, r, &req) {
		return
	}
	in := authuc.LoginInput{Email: req.Email, Password: req.Password, CaptchaToken: req.CaptchaToken}
	tokens, err := h.Service.Login(r.Context(), in, client(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.deliver(w, r, tokens)
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
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		problem.Write(w, r, http.StatusUnauthorized, i18n.ErrUnauthorized)
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

func (h *Auth) clearCookies(w http.ResponseWriter) {
	http.SetCookie(w, h.Cookies.New(cookie.AccessToken, "", cookie.RootPath, true, -time.Second))
	http.SetCookie(w, h.Cookies.New(cookie.RefreshToken, "", cookie.RefreshPath, true, -time.Second))
}

func (h *Auth) isBrowser(r *http.Request) bool {
	return r.Header.Get("Origin") != "" || h.Cookies.HasAuth(r)
}

func (h *Auth) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Auth) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := dto.Decode(r, v); err != nil {
		h.fail(w, r, err)
		return false
	}
	return true
}

// fail maps use case errors to problem responses.
func (h *Auth) fail(w http.ResponseWriter, r *http.Request, err error) {
	var (
		decodeErr *dto.Error
		invalid   *domain.ValidationError
		limited   *authuc.RateLimitedError
	)
	switch {
	case errors.As(err, &decodeErr):
		writeFieldProblem(w, r, decodeErr.Status, decodeErr.Key, decodeErr.Fields)
	case errors.As(err, &invalid):
		writeFieldProblem(w, r, http.StatusUnprocessableEntity, i18n.ErrValidationFailed, invalid.Fields)
	case errors.As(err, &limited):
		middleware.WriteRateLimited(w, r, limited.RetryAfter)
	case errors.Is(err, authuc.ErrInvalidCredentials):
		problem.Write(w, r, http.StatusUnauthorized, i18n.ErrInvalidCredentials)
	case errors.Is(err, authuc.ErrUnauthorized):
		problem.Write(w, r, http.StatusUnauthorized, i18n.ErrUnauthorized)
	case errors.Is(err, authuc.ErrEmailNotVerified):
		problem.Write(w, r, http.StatusForbidden, i18n.ErrEmailNotVerified)
	case errors.Is(err, authuc.ErrCaptchaRequired):
		problem.Write(w, r, http.StatusForbidden, i18n.ErrCaptchaRequired)
	case errors.Is(err, authuc.ErrCaptchaInvalid):
		problem.Write(w, r, http.StatusForbidden, i18n.ErrCaptchaInvalid)
	case errors.Is(err, authuc.ErrTokenInvalid):
		problem.Write(w, r, http.StatusBadRequest, i18n.ErrTokenInvalid)
	case errors.Is(err, authuc.ErrRefreshConflict):
		problem.Write(w, r, http.StatusConflict, i18n.ErrRefreshConflict)
	case errors.Is(err, authuc.ErrUnavailable):
		h.Logger.ErrorContext(r.Context(), "auth dependency unavailable", slog.String("error", err.Error()))
		problem.Write(w, r, http.StatusServiceUnavailable, i18n.ErrServiceUnavailable)
	default:
		h.Logger.ErrorContext(r.Context(), "auth request failed", slog.String("error", err.Error()))
		problem.Write(w, r, http.StatusInternalServerError, i18n.ErrInternal)
	}
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
