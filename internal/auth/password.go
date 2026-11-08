// Package auth holds the credential primitives: password hashing, breach
// checks, access tokens (JWT) and opaque tokens (§6.1, §6.2).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are the Argon2id cost parameters.
type Params struct {
	// Memory is in KiB.
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams follow §6.1: m=64 MiB, t=3, p=2.
var DefaultParams = Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}

// Bounds accepted when reading a stored hash, so a corrupted or planted
// record cannot make a single verification allocate gigabytes.
const (
	maxMemoryKiB   = 1 << 20 // 1 GiB
	maxIterations  = 16
	maxParallelism = 16
	minSaltLength  = 8
	maxSaltLength  = 64
	minKeyLength   = 16
	maxKeyLength   = 64
)

// ErrMalformedHash means a stored hash is not a PHC Argon2id string this package can read.
var ErrMalformedHash = errors.New("malformed password hash")

var b64 = base64.RawStdEncoding

// Hasher hashes and verifies passwords with Argon2id. A semaphore bounds how
// many hashes run at once: each one takes Params.Memory, so unbounded
// parallel logins would be a memory exhaustion attack.
type Hasher struct {
	params Params
	slots  chan struct{}
	dummy  string
}

// NewHasher returns a hasher running at most concurrency hashes at once.
func NewHasher(params Params, concurrency int) (*Hasher, error) {
	if concurrency < 1 {
		return nil, errors.New("hash concurrency must be at least 1")
	}
	if params.Memory < 8*uint32(params.Parallelism) || params.Iterations < 1 || params.Parallelism < 1 ||
		params.SaltLength < minSaltLength || params.KeyLength < minKeyLength {
		return nil, errors.New("invalid argon2id parameters")
	}
	h := &Hasher{params: params, slots: make(chan struct{}, concurrency)}
	dummy, err := h.Hash(context.Background(), rand.Text())
	if err != nil {
		return nil, err
	}
	h.dummy = dummy
	return h, nil
}

// Hash returns the PHC string of password:
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<key>.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, h.params.SaltLength)
	_, _ = rand.Read(salt) // crypto/rand.Read never fails on supported platforms
	release, err := h.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	key := argon2.IDKey([]byte(password), salt, h.params.Iterations, h.params.Memory, h.params.Parallelism, h.params.KeyLength)
	return encode(h.params, salt, key), nil
}

// Verify reports whether password matches encoded, and whether encoded was
// made with other parameters and should be replaced by a fresh Hash.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (ok, needsRehash bool, err error) {
	params, salt, key, err := decode(encoded)
	if err != nil {
		return false, false, err
	}
	release, err := h.acquire(ctx)
	if err != nil {
		return false, false, err
	}
	defer release()
	got := argon2.IDKey([]byte(password), salt, params.Iterations, params.Memory, params.Parallelism, uint32(len(key))) //nolint:gosec // G115: len(key) is bounded by decode
	if subtle.ConstantTimeCompare(got, key) != 1 {
		return false, false, nil
	}
	return true, params != h.params, nil
}

// VerifyDummy runs a verification that always fails, against a hash made at
// startup. Login calls it when the email is unknown, so that answer takes as
// long as a wrong password for a real account.
func (h *Hasher) VerifyDummy(ctx context.Context, password string) {
	_, _, _ = h.Verify(ctx, password, h.dummy)
}

func (h *Hasher) acquire(ctx context.Context) (func(), error) {
	select {
	case h.slots <- struct{}{}:
		return func() { <-h.slots }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for a password hash slot: %w", ctx.Err())
	}
}

func encode(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism, b64.EncodeToString(salt), b64.EncodeToString(key))
}

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Params{}, nil, nil, ErrMalformedHash
	}
	var p Params
	for kv := range strings.SplitSeq(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return Params{}, nil, nil, ErrMalformedHash
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return Params{}, nil, nil, ErrMalformedHash
		}
		switch k {
		case "m":
			p.Memory = uint32(n)
		case "t":
			p.Iterations = uint32(n)
		case "p":
			if n > maxParallelism {
				return Params{}, nil, nil, ErrMalformedHash
			}
			p.Parallelism = uint8(n)
		default:
			return Params{}, nil, nil, ErrMalformedHash
		}
	}
	if p.Memory < 8*uint32(p.Parallelism) || p.Memory > maxMemoryKiB || p.Iterations < 1 || p.Iterations > maxIterations || p.Parallelism < 1 {
		return Params{}, nil, nil, ErrMalformedHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < minSaltLength || len(salt) > maxSaltLength {
		return Params{}, nil, nil, ErrMalformedHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < minKeyLength || len(key) > maxKeyLength {
		return Params{}, nil, nil, ErrMalformedHash
	}
	p.SaltLength = uint32(len(salt)) //nolint:gosec // G115: bounded above
	p.KeyLength = uint32(len(key))   //nolint:gosec // G115: bounded above
	return p, salt, key, nil
}
