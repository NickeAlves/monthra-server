package auth

import (
	"errors"
	"fmt"
	"time"
)

// Errors the HTTP layer maps to problem responses.
var (
	// ErrInvalidCredentials is the only answer to a wrong email or password.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrEmailNotVerified is returned after a correct password on an unconfirmed account.
	ErrEmailNotVerified = errors.New("email not verified")
	// ErrTokenInvalid covers unknown, used and expired emailed tokens alike.
	ErrTokenInvalid = errors.New("invalid or expired token")
	// ErrUnauthorized means the refresh token or session can no longer be used.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrRefreshConflict means the refresh token was rotated moments ago by a
	// concurrent request (another tab). The client retries with the cookie
	// that request received.
	ErrRefreshConflict = errors.New("refresh token already rotated")
	// ErrCaptchaRequired asks the client to solve a challenge and retry.
	ErrCaptchaRequired = errors.New("captcha required")
	// ErrCaptchaInvalid means the challenge answer was rejected.
	ErrCaptchaInvalid = errors.New("invalid captcha")
	// ErrUnavailable means a store that guards credentials is down; the
	// request is refused rather than let through (fail closed).
	ErrUnavailable = errors.New("auth dependency unavailable")
)

// RateLimitedError means the caller must wait RetryAfter.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("rate limited, retry after %s", e.RetryAfter)
}

func unavailable(err error) error { return fmt.Errorf("%w: %w", ErrUnavailable, err) }
