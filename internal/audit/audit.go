// Package audit records security-sensitive events (§6.6) in audit_logs.
// Entries carry who and from where, never the email address, password or token.
package audit

import (
	"context"
	"log/slog"
	"time"
	"unicode/utf8"
)

// Event names an audited action.
type Event string

// Audited events.
const (
	EventRegister             Event = "register"
	EventEmailVerified        Event = "email_verified"
	EventLoginSuccess         Event = "login_success"
	EventLoginFailure         Event = "login_failure"
	EventLogout               Event = "logout"
	EventRefreshReuseDetected Event = "refresh_reuse_detected"
	EventPasswordResetRequest Event = "password_reset_requested"
	EventPasswordReset        Event = "password_reset"
	EventReauthFailure        Event = "reauth_failure"
	EventMFASetup             Event = "mfa_setup"
	EventMFAEnabled           Event = "mfa_enabled"
	EventMFADisabled          Event = "mfa_disabled"
	EventMFAFailure           Event = "mfa_failure"
	EventMFARecoveryCodeUsed  Event = "mfa_recovery_code_used"
)

// maxUserAgent bounds the stored user agent.
const maxUserAgent = 256

// writeTimeout keeps a slow database from holding the request.
const writeTimeout = 2 * time.Second

// Entry is one audit log record.
type Entry struct {
	Event Event
	// UserID is empty when the actor is unknown (for example a failed login
	// for an address that has no account).
	UserID    string
	SessionID string
	IP        string
	UserAgent string
	// Reason qualifies failures with a short constant, never user input.
	Reason    string
	CreatedAt time.Time
}

// Store persists entries.
type Store interface {
	Insert(ctx context.Context, e Entry) error
}

// Recorder writes entries to a Store. A failed write is logged and never
// fails the request that caused it.
type Recorder struct {
	store  Store
	logger *slog.Logger
	now    func() time.Time
}

// NewRecorder returns a recorder on store.
func NewRecorder(store Store, logger *slog.Logger) *Recorder {
	return &Recorder{store: store, logger: logger, now: time.Now}
}

// Record stores e, stamping it and truncating the user agent.
func (r *Recorder) Record(ctx context.Context, e Entry) {
	e.CreatedAt = r.now().UTC()
	e.UserAgent = truncate(e.UserAgent, maxUserAgent)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	if err := r.store.Insert(ctx, e); err != nil {
		r.logger.ErrorContext(ctx, "write audit log", slog.String("event", string(e.Event)), slog.String("error", err.Error()))
	}
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
