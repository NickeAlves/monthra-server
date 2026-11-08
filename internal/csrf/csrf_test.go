package csrf

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSigner(t *testing.T, fill byte) Signer {
	t.Helper()
	s, err := NewSigner(bytes.Repeat([]byte{fill}, 32))
	require.NoError(t, err)
	return s
}

func TestIssueAndValidate(t *testing.T) {
	s := newSigner(t, 1)
	token := s.Issue()

	assert.True(t, s.Valid(token))
	assert.NotEqual(t, token, s.Issue(), "every token is random")
	assert.False(t, newSigner(t, 2).Valid(token), "another key rejects it")
}

func TestValidRejectsTampering(t *testing.T) {
	s := newSigner(t, 1)
	token := s.Issue()
	nonce, mac, _ := strings.Cut(token, ".")

	for name, bad := range map[string]string{
		"empty":           "",
		"no separator":    nonce + mac,
		"swapped nonce":   strings.Replace(token, nonce[:4], "AAAA", 1),
		"truncated mac":   nonce + "." + mac[:10],
		"not base64":      "!!!." + mac,
		"short nonce":     "AAAA." + mac,
		"oversized input": strings.Repeat("A", 200),
	} {
		assert.False(t, s.Valid(bad), name)
	}
}

func TestNewSignerNeedsLongKey(t *testing.T) {
	_, err := NewSigner([]byte("short"))
	assert.Error(t, err)
	assert.False(t, Signer{}.Valid(newSigner(t, 1).Issue()), "the zero signer never validates")
}
