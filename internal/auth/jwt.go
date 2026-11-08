package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Audience is the aud claim of access tokens.
const Audience = "monthra-api"

// ClockLeeway tolerates clock drift between API instances.
const ClockLeeway = 30 * time.Second

// maxTokenLength bounds what Parse looks at (a token is ~400 characters).
const maxTokenLength = 2048

// ErrInvalidToken is returned for any token that does not verify, whatever the reason.
var ErrInvalidToken = errors.New("invalid access token")

// Claims are the access token claims (§6.2), plus sid, the session the token belongs to.
type Claims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// Validate requires the claims the API relies on, on top of the time-based
// checks the parser does.
func (c Claims) Validate() error {
	if c.Subject == "" || c.ID == "" || c.SessionID == "" || c.NotBefore == nil || c.IssuedAt == nil {
		return errors.New("missing sub, jti, sid, nbf or iat")
	}
	return nil
}

// AccessToken is a signed token and what callers need to know about it.
type AccessToken struct {
	Token     string
	ID        string
	ExpiresAt time.Time
}

// JWTOptions configures a JWT signer and verifier.
type JWTOptions struct {
	// Seeds maps each kid to its Ed25519 seed (32 bytes).
	Seeds     map[string][]byte
	ActiveKID string
	Issuer    string
	TTL       time.Duration
	// Now defaults to time.Now.
	Now func() time.Time
}

// JWT signs access tokens with EdDSA (Ed25519) under the active kid and
// verifies tokens signed under any kid of the ring, so keys can rotate.
type JWT struct {
	private   ed25519.PrivateKey
	activeKID string
	public    map[string]ed25519.PublicKey
	issuer    string
	ttl       time.Duration
	now       func() time.Time
	parser    *jwt.Parser
}

// NewJWT builds the signer and verifier.
func NewJWT(o JWTOptions) (*JWT, error) {
	if o.Issuer == "" || o.TTL <= 0 {
		return nil, errors.New("jwt: issuer and ttl are required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	j := &JWT{activeKID: o.ActiveKID, public: map[string]ed25519.PublicKey{}, issuer: o.Issuer, ttl: o.TTL, now: o.Now}
	for kid, seed := range o.Seeds {
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("jwt: key %q must be a %d-byte Ed25519 seed", kid, ed25519.SeedSize)
		}
		key := ed25519.NewKeyFromSeed(seed)
		j.public[kid] = key.Public().(ed25519.PublicKey)
		if kid == o.ActiveKID {
			j.private = key
		}
	}
	if j.private == nil {
		return nil, fmt.Errorf("jwt: active key %q is not in the key ring", o.ActiveKID)
	}
	j.parser = jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(o.Issuer),
		jwt.WithAudience(Audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(ClockLeeway),
		jwt.WithTimeFunc(o.Now),
		jwt.WithStrictDecoding(),
	)
	return j, nil
}

// TTL is the lifetime of the access tokens this signer issues.
func (j *JWT) TTL() time.Duration { return j.ttl }

// Issue signs an access token for userID in sessionID.
func (j *JWT) Issue(userID, sessionID string) (AccessToken, error) {
	now := j.now().UTC().Truncate(time.Second)
	exp := now.Add(j.ttl)
	id := newTokenID()
	claims := Claims{
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    j.issuer,
			Subject:   userID,
			Audience:  jwt.ClaimStrings{Audience},
			ExpiresAt: jwt.NewNumericDate(exp),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        id,
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	t.Header["kid"] = j.activeKID
	signed, err := t.SignedString(j.private)
	if err != nil {
		return AccessToken{}, fmt.Errorf("sign access token: %w", err)
	}
	return AccessToken{Token: signed, ID: id, ExpiresAt: exp}, nil
}

// Parse verifies token: the algorithm is pinned to EdDSA, the kid must be in
// the ring, and iss, aud, exp, nbf and iat are all checked.
func (j *JWT) Parse(token string) (Claims, error) {
	if token == "" || len(token) > maxTokenLength {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	_, err := j.parser.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		key, ok := j.public[kid]
		if !ok {
			return nil, fmt.Errorf("unknown kid %q", kid)
		}
		return key, nil
	})
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	return claims, nil
}

func newTokenID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return base64.RawURLEncoding.EncodeToString(b)
}
