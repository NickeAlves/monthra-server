package domain

import "time"

// Session is one signed-in device. Access tokens carry its id (sid), and its
// refresh token rotates on every use.
type Session struct {
	ID         string
	UserID     string
	UserAgent  string
	IP         string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// Active reports whether the session can still be refreshed at now.
func (s Session) Active(now time.Time) bool {
	return s.RevokedAt == nil && now.Before(s.ExpiresAt)
}

// RefreshToken is one link of a session's rotation chain, stored by hash.
// A used token stays until it expires, so presenting it again is detected as reuse.
type RefreshToken struct {
	TokenHash string
	SessionID string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// TokenPurpose says what a single-use emailed token is for.
type TokenPurpose string

// Token purposes.
const (
	PurposeVerifyEmail TokenPurpose = "verify_email"
)

// AuthToken is a single-use, short-lived token sent by email, stored by hash.
type AuthToken struct {
	TokenHash string
	UserID    string
	Purpose   TokenPurpose
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}
