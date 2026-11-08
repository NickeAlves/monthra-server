// Package auth implements sign-up, email verification, login, token refresh,
// logout, password recovery and TOTP two-step verification (§6.1, §6.2) on
// top of ports, so every rule is unit tested without a database.
package auth

import (
	"context"
	"time"

	"github.com/NickeAlves/monthra-server/internal/audit"
	core "github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/captcha"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/email"
	"github.com/NickeAlves/monthra-server/internal/ratelimit"
)

// Users persists accounts.
type Users interface {
	Create(ctx context.Context, u domain.User) (domain.User, error)
	ByEmail(ctx context.Context, email string) (domain.User, error)
	ByID(ctx context.Context, id string) (domain.User, error)
	MarkEmailVerified(ctx context.Context, id string, at time.Time) error
	UpdatePasswordHash(ctx context.Context, id, hash string, at time.Time) error
	// ResetPassword replaces the hash and marks the address verified.
	ResetPassword(ctx context.Context, id, hash string, at time.Time) error

	// The MFA updates are compare-and-set: false means the account was not
	// in the expected state (see the MongoDB implementation).
	SetPendingMFA(ctx context.Context, id, sealed string, expiresAt, at time.Time) (bool, error)
	EnableMFA(ctx context.Context, id, pendingSealed string, recoveryHashes []string, step int64, at time.Time) (bool, error)
	UseTOTPStep(ctx context.Context, id string, step int64) (bool, error)
	UseRecoveryCode(ctx context.Context, id, hash string) (bool, error)
	ReplaceMFASecret(ctx context.Context, id, oldSealed, newSealed string) (bool, error)
	DisableMFA(ctx context.Context, id string, at time.Time) (bool, error)
}

// Sessions persists signed-in devices.
type Sessions interface {
	Create(ctx context.Context, s domain.Session) (domain.Session, error)
	ByID(ctx context.Context, id string) (domain.Session, error)
	Touch(ctx context.Context, id string, lastUsed, expiresAt time.Time) error
	Revoke(ctx context.Context, id string, at time.Time) error
	// RevokeAllForUser ends every active session but except and returns their ids.
	RevokeAllForUser(ctx context.Context, userID, except string, at time.Time) ([]string, error)
}

// RefreshTokens persists each session's rotation chain.
type RefreshTokens interface {
	Create(ctx context.Context, t domain.RefreshToken) error
	Consume(ctx context.Context, hash string, now time.Time) (domain.RefreshToken, error)
	ByHash(ctx context.Context, hash string) (domain.RefreshToken, error)
}

// AuthTokens persists single-use emailed tokens.
type AuthTokens interface {
	Create(ctx context.Context, t domain.AuthToken) error
	Consume(ctx context.Context, hash string, purpose domain.TokenPurpose, now time.Time) (domain.AuthToken, error)
	InvalidateFor(ctx context.Context, userID string, purpose domain.TokenPurpose, now time.Time) error
}

// Revocations is the access token denylist.
type Revocations interface {
	DenyToken(ctx context.Context, jti string, ttl time.Duration) error
	RevokeSession(ctx context.Context, sid string, ttl time.Duration) error
	RevokeSessions(ctx context.Context, sids []string, ttl time.Duration) error
}

// Lockout counts failed logins and sign-ups.
type Lockout interface {
	Check(ctx context.Context, email, ip string) (core.LoginState, error)
	RecordFailure(ctx context.Context, email, ip string) (core.LoginState, error)
	Reset(ctx context.Context, email string) error
	CountRegistration(ctx context.Context, ip string) (int, error)
}

// MFAChallenges holds the logins waiting for a second factor, by token hash.
type MFAChallenges interface {
	Create(ctx context.Context, hash string, c core.MFAChallenge, ttl time.Duration) error
	// Attempt counts one more code tried and returns the challenge, or false when it is gone.
	Attempt(ctx context.Context, hash string) (core.MFAChallenge, bool, error)
	// Consume deletes the challenge and reports whether this call did.
	Consume(ctx context.Context, hash string) (bool, error)
}

// Sealer encrypts secrets at rest (AES-256-GCM in production).
type Sealer interface {
	Seal(plaintext, aad []byte) (string, error)
	Open(sealed string, aad []byte) (plaintext []byte, needsRotation bool, err error)
}

// Limiter is one rate limit policy.
type Limiter interface {
	Take(ctx context.Context, subject string) (ratelimit.Result, error)
}

// Hasher hashes and verifies passwords.
type Hasher interface {
	Hash(ctx context.Context, password string) (string, error)
	Verify(ctx context.Context, password, encoded string) (ok, needsRehash bool, err error)
	VerifyDummy(ctx context.Context, password string)
}

// TokenIssuer signs access tokens.
type TokenIssuer interface {
	Issue(userID, sessionID string) (core.AccessToken, error)
	TTL() time.Duration
}

// Mailer queues transactional emails; it never blocks.
type Mailer interface {
	Send(ctx context.Context, m email.Message)
}

// Auditor records security events; it never fails.
type Auditor interface {
	Record(ctx context.Context, e audit.Entry)
}

// Metrics counts auth outcomes.
type Metrics interface {
	Login(result string)
	RefreshReuse()
	PasswordReset()
	MFA(event string)
	RateLimitRejected(policy string)
}

// Deps are the collaborators of the Service.
type Deps struct {
	Users         Users
	Sessions      Sessions
	RefreshTokens RefreshTokens
	AuthTokens    AuthTokens
	Revocations   Revocations
	Lockout       Lockout
	MFAChallenges MFAChallenges
	Sealer        Sealer
	// EmailLimit is the per-address budget of the credential endpoints.
	EmailLimit Limiter
	// MailLimit caps the account emails (verification, sign-up attempt,
	// password reset) one address receives.
	MailLimit Limiter
	Captcha   captcha.Verifier
	Breach    core.BreachChecker
	Hasher    Hasher
	Tokens    TokenIssuer
	Mailer    Mailer
	Auditor   Auditor
	Metrics   Metrics
}
