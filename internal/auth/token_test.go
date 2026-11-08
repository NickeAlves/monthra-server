package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpaqueTokens(t *testing.T) {
	a, b := NewOpaqueToken(), NewOpaqueToken()
	assert.NotEqual(t, a, b)
	assert.Len(t, a, 43)
	assert.True(t, ValidOpaqueToken(a))

	assert.False(t, ValidOpaqueToken(""))
	assert.False(t, ValidOpaqueToken(a[:42]))
	assert.False(t, ValidOpaqueToken(a+"A"))
	assert.False(t, ValidOpaqueToken(strings.Repeat("*", 43)), "not base64url")
	assert.False(t, ValidOpaqueToken(strings.Repeat("A", 42)+"B"), "non-canonical trailing bits")
}

func TestHashToken(t *testing.T) {
	assert.Equal(t, "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae", HashToken("foo"))
	assert.Equal(t, HashToken("foo"), HashToken("foo"))
	assert.NotEqual(t, HashToken("foo"), HashToken("bar"))
}

func TestHashIdentifier(t *testing.T) {
	h := HashIdentifier("email", "ana@example.com")
	assert.Len(t, h, 32)
	assert.Equal(t, h, HashIdentifier("email", "ana@example.com"))
	assert.NotEqual(t, h, HashIdentifier("other", "ana@example.com"), "the label separates uses")
	assert.NotContains(t, h, "ana")
}
