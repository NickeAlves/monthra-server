package auth

import (
	"context"
	"time"
)

// Principal is the authenticated caller of a request, taken from a verified access token.
type Principal struct {
	UserID    string
	SessionID string
	// TokenID is the jti of the access token, denylisted on logout.
	TokenID string
	// ExpiresAt is when the access token stops being accepted.
	ExpiresAt time.Time
}

// PrincipalFromClaims maps verified claims to a principal.
func PrincipalFromClaims(c Claims) Principal {
	p := Principal{UserID: c.Subject, SessionID: c.SessionID, TokenID: c.ID}
	if c.ExpiresAt != nil {
		p.ExpiresAt = c.ExpiresAt.UTC()
	}
	return p
}

type principalKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored in ctx, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
