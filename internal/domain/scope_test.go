package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEditScope(t *testing.T) {
	for in, want := range map[string]EditScope{"": ScopeThis, "this": ScopeThis, "future": ScopeFuture, "all": ScopeAll} {
		got, err := ParseEditScope(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got)
	}
	for _, in := range []string{"ALL", "next", " this"} {
		_, err := ParseEditScope(in)
		assert.Equal(t, CodeInvalid, codeOf(t, err), in)
	}
}
