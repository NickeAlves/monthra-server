// Package csrf issues and checks signed double-submit CSRF tokens (§6.3).
//
// A token is base64url(nonce) "." base64url(HMAC-SHA256(key, label|nonce)).
// The browser gets it in a readable cookie and echoes it in X-CSRF-Token; the
// guard accepts a mutating request only when both match and the signature is
// valid, so a token forged without the key never passes.
package csrf

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

const (
	nonceBytes = 32
	// maxTokenLen bounds what Valid decodes (43 + 1 + 43 characters today).
	maxTokenLen = 128
	// label separates this MAC from any other use of the key.
	label = "monthra-csrf-v1|"
)

var enc = base64.RawURLEncoding

// Signer issues and verifies tokens with one key.
type Signer struct{ key []byte }

// NewSigner returns a signer for key, which must have at least 32 bytes.
func NewSigner(key []byte) (Signer, error) {
	if len(key) < 32 {
		return Signer{}, errors.New("csrf key must have at least 32 bytes")
	}
	return Signer{key: append([]byte(nil), key...)}, nil
}

// Issue returns a new random, signed token.
func (s Signer) Issue() string {
	nonce := make([]byte, nonceBytes)
	_, _ = rand.Read(nonce) // crypto/rand.Read never returns an error on supported platforms
	return enc.EncodeToString(nonce) + "." + enc.EncodeToString(s.mac(nonce))
}

// Valid reports whether token was issued with this key.
func (s Signer) Valid(token string) bool {
	if len(s.key) == 0 || len(token) > maxTokenLen {
		return false
	}
	noncePart, macPart, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	nonce, err := enc.DecodeString(noncePart)
	if err != nil || len(nonce) != nonceBytes {
		return false
	}
	mac, err := enc.DecodeString(macPart)
	if err != nil {
		return false
	}
	return hmac.Equal(mac, s.mac(nonce))
}

func (s Signer) mac(nonce []byte) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(label))
	h.Write(nonce)
	return h.Sum(nil)
}
