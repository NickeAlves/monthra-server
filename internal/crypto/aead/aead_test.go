package aead

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, KeyBytes) }

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := New(map[string][]byte{"e1": key(1)}, "e1")
	require.NoError(t, err)
	aad := AAD("users.mfa_secret", "u1")

	sealed, err := s.Seal([]byte("JBSWY3DPEHPK3PXP"), aad)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(sealed, "e1:"))
	assert.NotContains(t, sealed, "JBSWY3DPEHPK3PXP")

	again, err := s.Seal([]byte("JBSWY3DPEHPK3PXP"), aad)
	require.NoError(t, err)
	assert.NotEqual(t, sealed, again, "every seal uses a fresh nonce")

	plain, rotate, err := s.Open(sealed, aad)
	require.NoError(t, err)
	assert.Equal(t, []byte("JBSWY3DPEHPK3PXP"), plain)
	assert.False(t, rotate)
}

func TestOpenRejects(t *testing.T) {
	s, err := New(map[string][]byte{"e1": key(1)}, "e1")
	require.NoError(t, err)
	aad := AAD("users.mfa_secret", "u1")
	sealed, err := s.Seal([]byte("secret"), aad)
	require.NoError(t, err)
	kid, encoded, _ := strings.Cut(sealed, ":")
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	raw[len(raw)-1] ^= 0x01
	tampered := kid + ":" + base64.RawURLEncoding.EncodeToString(raw)

	for name, tc := range map[string]struct {
		sealed string
		aad    []byte
		want   error
	}{
		"other owner":   {sealed, AAD("users.mfa_secret", "u2"), ErrAuthentication},
		"other field":   {sealed, AAD("bank_connections.provider_id", "u1"), ErrAuthentication},
		"tampered":      {tampered, aad, ErrAuthentication},
		"unknown kid":   {"e9:" + encoded, aad, ErrUnknownKey},
		"no kid":        {encoded, aad, ErrMalformed},
		"empty kid":     {":" + encoded, aad, ErrMalformed},
		"not base64":    {"e1:***", aad, ErrMalformed},
		"padded base64": {"e1:" + encoded + "==", aad, ErrMalformed},
		"too short":     {"e1:" + base64.RawURLEncoding.EncodeToString([]byte("short")), aad, ErrMalformed},
		"empty":         {"", aad, ErrMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := s.Open(tc.sealed, tc.aad)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestRotation(t *testing.T) {
	old, err := New(map[string][]byte{"e1": key(1)}, "e1")
	require.NoError(t, err)
	aad := AAD("users.mfa_secret", "u1")
	sealed, err := old.Seal([]byte("secret"), aad)
	require.NoError(t, err)

	rotated, err := New(map[string][]byte{"e1": key(1), "e2": key(2)}, "e2")
	require.NoError(t, err)
	plain, rotate, err := rotated.Open(sealed, aad)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), plain)
	assert.True(t, rotate, "values sealed with an old key should be sealed again")

	resealed, err := rotated.Seal(plain, aad)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(resealed, "e2:"))
	_, rotate, err = rotated.Open(resealed, aad)
	require.NoError(t, err)
	assert.False(t, rotate)

	_, _, err = old.Open(resealed, aad)
	assert.ErrorIs(t, err, ErrUnknownKey, "a ring without the new key cannot open it")
}

func TestNewRejectsBadRings(t *testing.T) {
	for name, tc := range map[string]struct {
		keys   map[string][]byte
		active string
	}{
		"active missing": {map[string][]byte{"e1": key(1)}, "e2"},
		"short key":      {map[string][]byte{"e1": key(1)[:16]}, "e1"},
		"colon in kid":   {map[string][]byte{"e:1": key(1)}, "e:1"},
		"empty ring":     {map[string][]byte{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(tc.keys, tc.active)
			assert.Error(t, err)
		})
	}
}
