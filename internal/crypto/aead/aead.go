// Package aead encrypts sensitive fields at rest with AES-256-GCM (§6.6),
// under a key ring so keys can be rotated without re-encrypting everything
// at once.
//
// A sealed value is "kid:base64url(nonce|ciphertext|tag)". The additional
// data binds it to its owner and field (see AAD): a value copied into another
// document or field no longer opens.
package aead

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// KeyBytes is the AES-256 key size.
const KeyBytes = 32

// Errors returned by Open.
var (
	// ErrMalformed means the value is not something Seal produced.
	ErrMalformed = errors.New("aead: malformed sealed value")
	// ErrUnknownKey means the value was sealed with a kid that is not in the ring.
	ErrUnknownKey = errors.New("aead: unknown key id")
	// ErrAuthentication means the value was tampered with, or opened with the wrong additional data.
	ErrAuthentication = errors.New("aead: message authentication failed")
)

// Sealer seals with the active key and opens with any key of the ring.
type Sealer struct {
	active string
	byKID  map[string]cipher.AEAD
}

// New builds a sealer from 32-byte keys by id.
func New(keys map[string][]byte, activeKID string) (*Sealer, error) {
	if _, ok := keys[activeKID]; !ok {
		return nil, fmt.Errorf("aead: active key %q is not in the ring", activeKID)
	}
	s := &Sealer{active: activeKID, byKID: make(map[string]cipher.AEAD, len(keys))}
	for kid, key := range keys {
		if kid == "" || strings.Contains(kid, ":") {
			return nil, fmt.Errorf("aead: invalid key id %q", kid)
		}
		if len(key) != KeyBytes {
			return nil, fmt.Errorf("aead: key %q must be %d bytes", kid, KeyBytes)
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("aead: key %q: %w", kid, err)
		}
		// Random 96-bit nonces, prepended by Seal. Safe up to 2^32 values per
		// key, far beyond what these fields reach between rotations.
		gcm, err := cipher.NewGCMWithRandomNonce(block)
		if err != nil {
			return nil, fmt.Errorf("aead: key %q: %w", kid, err)
		}
		s.byKID[kid] = gcm
	}
	return s, nil
}

// AAD is the additional data of a field ("users.mfa_secret") owned by owner (a user id).
func AAD(field, owner string) []byte { return []byte(field + "|" + owner) }

// Seal encrypts plaintext under the active key.
func (s *Sealer) Seal(plaintext, aad []byte) (string, error) {
	ct := s.byKID[s.active].Seal(nil, nil, plaintext, aad)
	return s.active + ":" + base64.RawURLEncoding.EncodeToString(ct), nil
}

// Open decrypts a value from Seal. needsRotation reports that it was sealed
// with a key other than the active one, so the caller can seal it again.
func (s *Sealer) Open(sealed string, aad []byte) (plaintext []byte, needsRotation bool, err error) {
	kid, encoded, ok := strings.Cut(sealed, ":")
	if !ok || kid == "" {
		return nil, false, ErrMalformed
	}
	gcm, ok := s.byKID[kid]
	if !ok {
		return nil, false, ErrUnknownKey
	}
	ct, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(ct) < gcm.Overhead() {
		return nil, false, ErrMalformed
	}
	plaintext, err = gcm.Open(nil, nil, ct, aad)
	if err != nil {
		return nil, false, ErrAuthentication
	}
	return plaintext, kid != s.active, nil
}
