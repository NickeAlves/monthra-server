package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
	"github.com/NickeAlves/monthra-server/internal/http/middleware"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
	"github.com/NickeAlves/monthra-server/internal/usecase/profile"
)

// writeError maps use case errors to problem responses, for every handler.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
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
	// A token whose account is gone is answered like any invalid session.
	case errors.Is(err, authuc.ErrUnauthorized), errors.Is(err, profile.ErrAccountNotFound):
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
	// 403, not 401: the session is fine, only the step-up or the code failed,
	// and a 401 would make clients refresh or sign out.
	case errors.Is(err, authuc.ErrPasswordIncorrect):
		problem.Write(w, r, http.StatusForbidden, i18n.ErrPasswordIncorrect)
	case errors.Is(err, authuc.ErrMFACodeInvalid):
		problem.Write(w, r, http.StatusForbidden, i18n.ErrMFACodeInvalid)
	case errors.Is(err, authuc.ErrMFAChallengeInvalid):
		problem.Write(w, r, http.StatusUnauthorized, i18n.ErrMFAChallengeInvalid)
	case errors.Is(err, authuc.ErrMFAAlreadyEnabled):
		problem.Write(w, r, http.StatusConflict, i18n.ErrMFAAlreadyEnabled)
	case errors.Is(err, authuc.ErrMFANotEnabled):
		problem.Write(w, r, http.StatusConflict, i18n.ErrMFANotEnabled)
	case errors.Is(err, authuc.ErrMFASetupRequired):
		problem.Write(w, r, http.StatusConflict, i18n.ErrMFASetupRequired)
	// Another user's session looks exactly like one that does not exist.
	case errors.Is(err, authuc.ErrSessionNotFound):
		problem.Write(w, r, http.StatusNotFound, i18n.ErrNotFound)
	// Feature records (accounts, categories, ...): another user's record
	// looks exactly like one that does not exist. The auth use case maps its
	// own lookups before they get here, so this never hides an auth failure.
	case errors.Is(err, domain.ErrNotFound):
		problem.Write(w, r, http.StatusNotFound, i18n.ErrNotFound)
	case errors.Is(err, domain.ErrLimitReached):
		problem.Write(w, r, http.StatusConflict, i18n.ErrLimitReached)
	case errors.Is(err, domain.ErrAccountInUse):
		problem.Write(w, r, http.StatusConflict, i18n.ErrAccountInUse)
	case errors.Is(err, domain.ErrCategoryInUse):
		problem.Write(w, r, http.StatusConflict, i18n.ErrCategoryInUse)
	case errors.Is(err, domain.ErrUsedByRecurrence):
		problem.Write(w, r, http.StatusConflict, i18n.ErrUsedByRecurrence)
	case errors.Is(err, domain.ErrBudgetExists):
		problem.Write(w, r, http.StatusConflict, i18n.ErrBudgetExists)
	case errors.Is(err, domain.ErrExceedsPosition):
		problem.Write(w, r, http.StatusConflict, i18n.ErrExceedsPosition)
	case errors.Is(err, authuc.ErrUnavailable):
		logger.ErrorContext(r.Context(), "auth dependency unavailable", slog.String("error", err.Error()))
		problem.Write(w, r, http.StatusServiceUnavailable, i18n.ErrServiceUnavailable)
	default:
		logger.ErrorContext(r.Context(), "request failed", slog.String("error", err.Error()))
		problem.Write(w, r, http.StatusInternalServerError, i18n.ErrInternal)
	}
}
