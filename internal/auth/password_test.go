package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastParams keep the tests quick; DefaultParams cost ~75 ms per hash (~500 ms under -race).
var fastParams = Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func newFastHasher(t *testing.T, concurrency int) *Hasher {
	t.Helper()
	h, err := NewHasher(fastParams, concurrency)
	require.NoError(t, err)
	return h
}

func TestHashAndVerify(t *testing.T) {
	ctx := context.Background()
	h := newFastHasher(t, 2)
	password := strings.Repeat("correct horse ", 2)

	encoded, err := h.Hash(ctx, password)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(encoded, "$argon2id$v=19$m=64,t=1,p=1$"), encoded)

	again, err := h.Hash(ctx, password)
	require.NoError(t, err)
	assert.NotEqual(t, encoded, again, "every hash gets a fresh salt")

	ok, rehash, err := h.Verify(ctx, password, encoded)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.False(t, rehash)

	ok, _, err = h.Verify(ctx, password+"!", encoded)
	require.NoError(t, err)
	assert.False(t, ok)

	h.VerifyDummy(ctx, password) // never matches, never panics
}

func TestDefaultParamsFormat(t *testing.T) {
	assert.Equal(t, "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$a2V5", encode(DefaultParams, []byte("salt"), []byte("key")))
}

func TestVerifyFlagsOutdatedParameters(t *testing.T) {
	ctx := context.Background()
	old, err := NewHasher(Params{Memory: 32, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32}, 1)
	require.NoError(t, err)
	password := strings.Repeat("battery staple ", 2)
	encoded, err := old.Hash(ctx, password)
	require.NoError(t, err)

	ok, rehash, err := newFastHasher(t, 1).Verify(ctx, password, encoded)
	require.NoError(t, err)
	assert.True(t, ok, "old hashes keep verifying")
	assert.True(t, rehash, "and are flagged for an upgrade")
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	h := newFastHasher(t, 1)
	valid, err := h.Hash(context.Background(), "irrelevant-value")
	require.NoError(t, err)
	parts := strings.Split(valid, "$")
	salt, key := parts[4], parts[5]

	for name, encoded := range map[string]string{
		"empty":             "",
		"bcrypt":            "$2a$10$abcdefghijklmnopqrstuv",
		"argon2i":           "$argon2i$v=19$m=64,t=1,p=1$" + salt + "$" + key,
		"old version":       "$argon2id$v=16$m=64,t=1,p=1$" + salt + "$" + key,
		"missing part":      "$argon2id$v=19$m=64,t=1,p=1$" + salt,
		"unknown param":     "$argon2id$v=19$m=64,t=1,p=1,x=2$" + salt + "$" + key,
		"non-numeric param": "$argon2id$v=19$m=lots,t=1,p=1$" + salt + "$" + key,
		"param without =":   "$argon2id$v=19$m64,t=1,p=1$" + salt + "$" + key,
		"huge memory":       "$argon2id$v=19$m=4194304,t=1,p=1$" + salt + "$" + key,
		"too many passes":   "$argon2id$v=19$m=64,t=99,p=1$" + salt + "$" + key,
		"zero parallelism":  "$argon2id$v=19$m=64,t=1,p=0$" + salt + "$" + key,
		"huge parallelism":  "$argon2id$v=19$m=64,t=1,p=200$" + salt + "$" + key,
		"memory below 8p":   "$argon2id$v=19$m=8,t=1,p=2$" + salt + "$" + key,
		"salt not base64":   "$argon2id$v=19$m=64,t=1,p=1$***$" + key,
		"short salt":        "$argon2id$v=19$m=64,t=1,p=1$c2FsdA$" + key,
		"short key":         "$argon2id$v=19$m=64,t=1,p=1$" + salt + "$a2V5",
	} {
		ok, _, err := h.Verify(context.Background(), "irrelevant-value", encoded)
		assert.ErrorIs(t, err, ErrMalformedHash, name)
		assert.False(t, ok, name)
	}
}

func TestHasherBoundsConcurrency(t *testing.T) {
	h := newFastHasher(t, 1)
	stored, err := h.Hash(context.Background(), "irrelevant-value")
	require.NoError(t, err)
	h.slots <- struct{}{} // take the only slot

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = h.Hash(ctx, "irrelevant-value")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, _, err = h.Verify(ctx, "irrelevant-value", stored)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	<-h.slots
	_, err = h.Hash(context.Background(), "irrelevant-value")
	assert.NoError(t, err)
}

func TestNewHasherRejectsBadSettings(t *testing.T) {
	_, err := NewHasher(fastParams, 0)
	assert.Error(t, err)
	_, err = NewHasher(Params{Memory: 64, Iterations: 0, Parallelism: 1, SaltLength: 16, KeyLength: 32}, 1)
	assert.Error(t, err)
	_, err = NewHasher(Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 4, KeyLength: 32}, 1)
	assert.Error(t, err)
}
