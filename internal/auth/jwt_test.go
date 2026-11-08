package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testIssuer = "monthra-server:local"

var (
	seed1 = bytes.Repeat([]byte{1}, ed25519.SeedSize)
	seed2 = bytes.Repeat([]byte{2}, ed25519.SeedSize)
	t0    = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
)

func newJWT(t *testing.T, seeds map[string][]byte, active string, now *time.Time) *JWT {
	t.Helper()
	j, err := NewJWT(JWTOptions{Seeds: seeds, ActiveKID: active, Issuer: testIssuer, TTL: 15 * time.Minute, Now: func() time.Time { return *now }})
	require.NoError(t, err)
	return j
}

func TestIssueAndParse(t *testing.T) {
	now := t0
	j := newJWT(t, map[string][]byte{"k1": seed1}, "k1", &now)
	assert.Equal(t, 15*time.Minute, j.TTL())

	at, err := j.Issue("user-1", "session-1")
	require.NoError(t, err)
	assert.Equal(t, t0.Add(15*time.Minute), at.ExpiresAt)
	assert.NotEmpty(t, at.ID)

	claims, err := j.Parse(at.Token)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.Subject)
	assert.Equal(t, "session-1", claims.SessionID)
	assert.Equal(t, at.ID, claims.ID)
	assert.Equal(t, testIssuer, claims.Issuer)
	assert.Equal(t, jwt.ClaimStrings{Audience}, claims.Audience)

	p := PrincipalFromClaims(claims)
	assert.Equal(t, Principal{UserID: "user-1", SessionID: "session-1", TokenID: at.ID, ExpiresAt: at.ExpiresAt}, p)

	header, _, _ := strings.Cut(at.Token, ".")
	parsed, _, err := jwt.NewParser().ParseUnverified(at.Token, &Claims{})
	require.NoError(t, err)
	assert.Equal(t, "EdDSA", parsed.Method.Alg())
	assert.Equal(t, "k1", parsed.Header["kid"], header)

	other, err := j.Issue("user-1", "session-1")
	require.NoError(t, err)
	assert.NotEqual(t, at.ID, other.ID, "every token gets its own jti")
}

func TestParseHonorsTime(t *testing.T) {
	now := t0
	j := newJWT(t, map[string][]byte{"k1": seed1}, "k1", &now)
	at, err := j.Issue("user-1", "session-1")
	require.NoError(t, err)

	now = t0.Add(15*time.Minute + 20*time.Second)
	_, err = j.Parse(at.Token)
	assert.NoError(t, err, "within the leeway")

	now = t0.Add(16 * time.Minute)
	_, err = j.Parse(at.Token)
	assert.ErrorIs(t, err, ErrInvalidToken, "expired")

	now = t0.Add(-time.Minute)
	_, err = j.Parse(at.Token)
	assert.ErrorIs(t, err, ErrInvalidToken, "not valid yet")
}

func TestParseSupportsKeyRotation(t *testing.T) {
	now := t0
	old := newJWT(t, map[string][]byte{"k1": seed1}, "k1", &now)
	rotated := newJWT(t, map[string][]byte{"k1": seed1, "k2": seed2}, "k2", &now)
	dropped := newJWT(t, map[string][]byte{"k2": seed2}, "k2", &now)

	oldToken, err := old.Issue("user-1", "session-1")
	require.NoError(t, err)
	_, err = rotated.Parse(oldToken.Token)
	assert.NoError(t, err, "tokens of the previous key keep verifying")
	_, err = dropped.Parse(oldToken.Token)
	assert.ErrorIs(t, err, ErrInvalidToken, "until the key leaves the ring")

	newToken, err := rotated.Issue("user-1", "session-1")
	require.NoError(t, err)
	_, err = old.Parse(newToken.Token)
	assert.ErrorIs(t, err, ErrInvalidToken, "the new kid is unknown to the old ring")
}

func TestParseRejectsForgedTokens(t *testing.T) {
	now := t0
	j := newJWT(t, map[string][]byte{"k1": seed1}, "k1", &now)
	valid := Claims{
		SessionID: "session-1",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: testIssuer, Subject: "user-1", Audience: jwt.ClaimStrings{Audience}, ID: "jti-1",
			ExpiresAt: jwt.NewNumericDate(t0.Add(time.Minute)), NotBefore: jwt.NewNumericDate(t0), IssuedAt: jwt.NewNumericDate(t0),
		},
	}
	sign := func(method jwt.SigningMethod, key any, kid string, mutate func(*Claims)) string {
		c := valid
		if mutate != nil {
			mutate(&c)
		}
		tok := jwt.NewWithClaims(method, c)
		if kid != "" {
			tok.Header["kid"] = kid
		}
		s, err := tok.SignedString(key)
		require.NoError(t, err)
		return s
	}
	key1 := ed25519.NewKeyFromSeed(seed1)
	stranger := ed25519.NewKeyFromSeed(seed2)

	require.NotEmpty(t, sign(jwt.SigningMethodEdDSA, key1, "k1", nil))
	_, err := j.Parse(sign(jwt.SigningMethodEdDSA, key1, "k1", nil))
	require.NoError(t, err, "the fixture itself is valid")

	for name, token := range map[string]string{
		"alg none":          sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, "k1", nil),
		"hmac with pubkey":  sign(jwt.SigningMethodHS256, []byte(key1.Public().(ed25519.PublicKey)), "k1", nil),
		"unknown kid":       sign(jwt.SigningMethodEdDSA, key1, "k9", nil),
		"no kid":            sign(jwt.SigningMethodEdDSA, key1, "", nil),
		"foreign key":       sign(jwt.SigningMethodEdDSA, stranger, "k1", nil),
		"wrong issuer":      sign(jwt.SigningMethodEdDSA, key1, "k1", func(c *Claims) { c.Issuer = "monthra-server:staging" }),
		"wrong audience":    sign(jwt.SigningMethodEdDSA, key1, "k1", func(c *Claims) { c.Audience = jwt.ClaimStrings{"other"} }),
		"no expiry":         sign(jwt.SigningMethodEdDSA, key1, "k1", func(c *Claims) { c.ExpiresAt = nil }),
		"issued in future":  sign(jwt.SigningMethodEdDSA, key1, "k1", func(c *Claims) { c.IssuedAt = jwt.NewNumericDate(t0.Add(time.Hour)) }),
		"no subject":        sign(jwt.SigningMethodEdDSA, key1, "k1", func(c *Claims) { c.Subject = "" }),
		"no session":        sign(jwt.SigningMethodEdDSA, key1, "k1", func(c *Claims) { c.SessionID = "" }),
		"no jti":            sign(jwt.SigningMethodEdDSA, key1, "k1", func(c *Claims) { c.ID = "" }),
		"no not-before":     sign(jwt.SigningMethodEdDSA, key1, "k1", func(c *Claims) { c.NotBefore = nil }),
		"empty":             "",
		"garbage":           "not.a.jwt",
		"oversized":         strings.Repeat("a", maxTokenLength+1),
		"tampered payload":  tamper(sign(jwt.SigningMethodEdDSA, key1, "k1", nil)),
		"trailing segments": sign(jwt.SigningMethodEdDSA, key1, "k1", nil) + ".extra",
	} {
		_, err := j.Parse(token)
		assert.ErrorIs(t, err, ErrInvalidToken, name)
	}
}

// tamper swaps the payload for one with another subject, keeping the signature.
func tamper(token string) string {
	parts := strings.Split(token, ".")
	other := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"sub": "admin"})
	s, _ := other.SigningString()
	parts[1] = strings.Split(s, ".")[1]
	return strings.Join(parts, ".")
}

func TestNewJWTRejectsBadOptions(t *testing.T) {
	for name, o := range map[string]JWTOptions{
		"no issuer":     {Seeds: map[string][]byte{"k1": seed1}, ActiveKID: "k1", TTL: time.Minute},
		"no ttl":        {Seeds: map[string][]byte{"k1": seed1}, ActiveKID: "k1", Issuer: testIssuer},
		"short seed":    {Seeds: map[string][]byte{"k1": seed1[:16]}, ActiveKID: "k1", Issuer: testIssuer, TTL: time.Minute},
		"missing a kid": {Seeds: map[string][]byte{"k1": seed1}, ActiveKID: "k2", Issuer: testIssuer, TTL: time.Minute},
	} {
		_, err := NewJWT(o)
		assert.Error(t, err, name)
	}
	j, err := NewJWT(JWTOptions{Seeds: map[string][]byte{"k1": seed1}, ActiveKID: "k1", Issuer: testIssuer, TTL: time.Minute})
	require.NoError(t, err)
	at, err := j.Issue("u", "s")
	require.NoError(t, err)
	_, err = j.Parse(at.Token)
	assert.NoError(t, err, "defaults to the real clock")
}

func TestPrincipalContext(t *testing.T) {
	_, ok := PrincipalFrom(context.Background())
	assert.False(t, ok)
	ctx := WithPrincipal(context.Background(), Principal{UserID: "u1"})
	p, ok := PrincipalFrom(ctx)
	assert.True(t, ok)
	assert.Equal(t, "u1", p.UserID)
}
