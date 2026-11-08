package auth

import (
	"encoding/base32"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rfcSecret is the SHA-1 seed of the RFC 6238 appendix B test vectors.
var rfcSecret = base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))

func TestMatchTOTPWithRFC6238Vectors(t *testing.T) {
	// The RFC lists 8-digit codes; 6-digit codes are their last six digits.
	for unix, code := range map[int64]string{
		59:         "287082",
		1111111109: "081804",
		1111111111: "050471",
		1234567890: "005924",
		2000000000: "279037",
	} {
		step, ok, err := MatchTOTP(rfcSecret, code, time.Unix(unix, 0))
		require.NoError(t, err)
		assert.True(t, ok, "t=%d", unix)
		assert.Equal(t, unix/30, step, "t=%d", unix)
	}
}

func TestMatchTOTPWindow(t *testing.T) {
	now := time.Unix(1_800_000_015, 0)
	current := now.Unix() / 30
	codeAt := func(step int64) string {
		c, err := totp.GenerateCodeCustom(rfcSecret, time.Unix(step*30, 0), totpOpts)
		require.NoError(t, err)
		return c
	}
	for offset, want := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		step, ok, err := MatchTOTP(rfcSecret, codeAt(current+offset), now)
		require.NoError(t, err)
		assert.Equal(t, want, ok, "offset %d", offset)
		if want {
			assert.Equal(t, current+offset, step, "offset %d", offset)
		}
	}

	step, ok, err := MatchTOTP(rfcSecret, " "+codeAt(current)[:3]+" "+codeAt(current)[3:], now)
	require.NoError(t, err)
	assert.True(t, ok, "spaces are ignored")
	assert.Equal(t, current, step)
}

func TestMatchTOTPRejectsMalformedInput(t *testing.T) {
	for _, code := range []string{"", "12345", "1234567", "abcdef", "12345a", "١٢٣٤٥٦"} {
		_, ok, err := MatchTOTP(rfcSecret, code, time.Unix(59, 0))
		require.NoError(t, err)
		assert.False(t, ok, "%q", code)
		assert.False(t, LooksLikeTOTPCode(code), "%q", code)
	}
	assert.True(t, LooksLikeTOTPCode("287 082"))

	_, _, err := MatchTOTP("not base32!", "287082", time.Unix(59, 0))
	assert.Error(t, err)
}

func TestNewTOTPKey(t *testing.T) {
	key, err := NewTOTPKey("Monthra (local)", "ana@example.com")
	require.NoError(t, err)
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(key.Secret)
	require.NoError(t, err)
	assert.Len(t, raw, totpSecretBytes)

	u, err := url.Parse(key.URI)
	require.NoError(t, err)
	assert.Equal(t, "otpauth", u.Scheme)
	assert.Equal(t, "totp", u.Host)
	q := u.Query()
	assert.Equal(t, key.Secret, q.Get("secret"))
	assert.Equal(t, "Monthra (local)", q.Get("issuer"))
	assert.Equal(t, "SHA1", q.Get("algorithm"))
	assert.Equal(t, "6", q.Get("digits"))
	assert.Equal(t, "30", q.Get("period"))

	code, err := totp.GenerateCode(key.Secret, time.Now())
	require.NoError(t, err)
	_, ok, err := MatchTOTP(key.Secret, code, time.Now())
	require.NoError(t, err)
	assert.True(t, ok)

	other, err := NewTOTPKey("Monthra (local)", "ana@example.com")
	require.NoError(t, err)
	assert.NotEqual(t, key.Secret, other.Secret)
}

func TestRecoveryCodes(t *testing.T) {
	codes := NewRecoveryCodes(RecoveryCodeCount)
	require.Len(t, codes, RecoveryCodeCount)
	shape := regexp.MustCompile(`^[a-z2-7]{4}(-[a-z2-7]{4}){3}$`)
	seen := map[string]bool{}
	for _, c := range codes {
		assert.Regexp(t, shape, c)
		assert.False(t, seen[c], "codes are unique")
		seen[c] = true

		canonical, ok := NormalizeRecoveryCode(c)
		require.True(t, ok, c)
		assert.Len(t, canonical, 16)
		upper, ok := NormalizeRecoveryCode(" " + canonical[:8] + " " + canonical[8:] + " ")
		require.True(t, ok)
		assert.Equal(t, HashRecoveryCode(canonical), HashRecoveryCode(upper))
		assert.False(t, LooksLikeTOTPCode(c))
	}

	for _, bad := range []string{"", "abcd-efgh-ijkl", "abcd-efgh-ijkl-mnop-qrst", "abcd-efgh-ijkl-mno1", "abcd-efgh-ijkl-mn0p", "287082"} {
		_, ok := NormalizeRecoveryCode(bad)
		assert.False(t, ok, "%q", bad)
	}
	assert.NotEqual(t, HashToken("ABCDEFGHIJKLMNOP"), HashRecoveryCode("ABCDEFGHIJKLMNOP"), "recovery hashes are domain separated")
}
