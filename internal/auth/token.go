package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// opaqueTokenBytes is the entropy of refresh and email tokens (§6.1: 32 bytes).
const opaqueTokenBytes = 32

// OpaqueTokenLength is the length of an encoded opaque token.
var OpaqueTokenLength = base64.RawURLEncoding.EncodedLen(opaqueTokenBytes)

// NewOpaqueToken returns 32 random bytes, base64url-encoded.
func NewOpaqueToken() string {
	b := make([]byte, opaqueTokenBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return base64.RawURLEncoding.EncodeToString(b)
}

// ValidOpaqueToken reports whether s has the shape of a token from
// NewOpaqueToken, so junk input never reaches the database.
func ValidOpaqueToken(s string) bool {
	if len(s) != OpaqueTokenLength {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(b) == opaqueTokenBytes
}

// HashToken is how opaque tokens are stored and looked up. They carry 256
// random bits, so a fast unsalted hash is enough: there is nothing to guess.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// HashIdentifier hashes an identifier such as an email address before it
// becomes part of a Redis key or a log line.
func HashIdentifier(label, value string) string {
	sum := sha256.Sum256([]byte(label + "|" + value))
	return hex.EncodeToString(sum[:16])
}
