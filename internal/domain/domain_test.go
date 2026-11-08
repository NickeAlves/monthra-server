package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var v Validation
	v.Check("f", err)
	var ve *ValidationError
	require.ErrorAs(t, v.Err(), &ve)
	return ve.Fields[0].Code
}

func TestNormalizeEmail(t *testing.T) {
	for in, want := range map[string]string{
		"  Ana@Example.COM ":     "ana@example.com",
		"a.b+tag@sub.example.io": "a.b+tag@sub.example.io",
		"x@localhost":            "x@localhost",
	} {
		got, err := NormalizeEmail(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got)
	}

	for in, code := range map[string]string{
		"":                                       CodeRequired,
		"   ":                                    CodeRequired,
		"no-at-sign":                             CodeInvalid,
		"Ana <ana@example.com>":                  CodeInvalid,
		"ana@example.com (comment)":              CodeInvalid,
		"two@@example.com":                       CodeInvalid,
		"a@b@c.com":                              CodeInvalid,
		"@example.com":                           CodeInvalid,
		"ana@":                                   CodeInvalid,
		"ana@.example.com":                       CodeInvalid,
		"ana@example..com":                       CodeInvalid,
		"ana@example.com.":                       CodeInvalid,
		"line\nbreak@example.com":                CodeInvalid,
		strings.Repeat("a", 65) + "@example.com": CodeInvalid,
		"a@" + strings.Repeat("b", 252) + ".com": CodeTooLong,
	} {
		_, err := NormalizeEmail(in)
		require.Error(t, err, in)
		assert.Equal(t, code, codeOf(t, err), in)
	}
}

func TestValidatePassword(t *testing.T) {
	assert.NoError(t, ValidatePassword(strings.Repeat("a", MinPasswordLength)))
	assert.NoError(t, ValidatePassword(strings.Repeat("é", MaxPasswordLength)), "length counts characters, not bytes")
	assert.NoError(t, ValidatePassword("only lowercase words"), "no composition rules")

	for name, tc := range map[string]struct{ in, code string }{
		"empty":         {"", CodeRequired},
		"too short":     {strings.Repeat("a", MinPasswordLength-1), CodeTooShort},
		"short runes":   {strings.Repeat("é", MinPasswordLength-1), CodeTooShort},
		"too long":      {strings.Repeat("a", MaxPasswordLength+1), CodeTooLong},
		"invalid utf-8": {"abcdefghij\xff", CodeInvalid},
		"huge":          {strings.Repeat("a", 5*MaxPasswordLength), CodeInvalid},
	} {
		err := ValidatePassword(tc.in)
		require.Error(t, err, name)
		assert.Equal(t, tc.code, codeOf(t, err), name)
	}
}

func TestNormalizeName(t *testing.T) {
	got, err := NormalizeName("  Ana Souza ")
	require.NoError(t, err)
	assert.Equal(t, "Ana Souza", got)

	got, err = NormalizeName(strings.Repeat("ã", MaxNameLength))
	require.NoError(t, err)
	assert.Len(t, []rune(got), MaxNameLength)

	for name, tc := range map[string]struct{ in, code string }{
		"empty":             {"  ", CodeRequired},
		"too long":          {strings.Repeat("a", MaxNameLength+1), CodeTooLong},
		"control char":      {"Ana\x00", CodeInvalid},
		"newline":           {"Ana\nSouza", CodeInvalid},
		"bidi override":     {"Ana\u202eazuoS", CodeInvalid},
		"invalid utf-8":     {"Ana\xff", CodeInvalid},
		"zero width joiner": {"A\u200dna", CodeInvalid},
	} {
		_, err := NormalizeName(tc.in)
		require.Error(t, err, name)
		assert.Equal(t, tc.code, codeOf(t, err), name)
	}
}

func TestNewUserAndPreferences(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	u := NewUser("Ana", "ana@example.com", "hash", i18n.EsES, now)
	assert.Equal(t, "Ana", u.Name)
	assert.Equal(t, "ana@example.com", u.Email)
	assert.Equal(t, "hash", u.PasswordHash)
	assert.False(t, u.EmailVerified())
	assert.Equal(t, time.UTC, u.CreatedAt.Location())
	assert.Equal(t, u.CreatedAt, u.UpdatedAt)
	assert.Equal(t, Preferences{Locale: i18n.EsES, Theme: ThemeSystem, Currency: "BRL"}, u.Preferences)

	assert.Equal(t, i18n.Default, DefaultPreferences("fr-FR").Locale)

	verified := now
	u.EmailVerifiedAt = &verified
	assert.True(t, u.EmailVerified())
}

func TestSessionActive(t *testing.T) {
	now := time.Now()
	s := Session{ExpiresAt: now.Add(time.Hour)}
	assert.True(t, s.Active(now))
	assert.False(t, s.Active(now.Add(2*time.Hour)), "expired")
	revoked := now
	s.RevokedAt = &revoked
	assert.False(t, s.Active(now), "revoked")
}

func TestValidation(t *testing.T) {
	var v Validation
	assert.NoError(t, v.Err())

	v.Check("ok", nil)
	v.Check("email", &fieldErr{CodeInvalid})
	v.Check("other", errors.New("boom"))
	v.Add("password", CodeBreached)

	err := v.Err()
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, []FieldError{{"email", CodeInvalid}, {"other", CodeInvalid}, {"password", CodeBreached}}, ve.Fields)
	assert.Equal(t, "invalid input (email: invalid, other: invalid, password: breached)", err.Error())
}

func TestMFAState(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	later := now.Add(15 * time.Minute)
	var u User
	assert.False(t, u.MFAEnabled())
	assert.False(t, u.MFA.PendingActive(now))

	u.MFA = MFA{PendingSecret: "e1:x", PendingExpiresAt: &later}
	assert.True(t, u.MFA.PendingActive(now))
	assert.False(t, u.MFA.PendingActive(later), "expires at PendingExpiresAt")
	assert.False(t, MFA{PendingSecret: "e1:x"}.PendingActive(now), "no expiry means no setup")
	assert.False(t, MFA{PendingExpiresAt: &later}.PendingActive(now), "no secret means no setup")

	u.MFA = MFA{Secret: "e1:x", EnabledAt: &now}
	assert.True(t, u.MFAEnabled())
}

func TestParseThemeAndCurrency(t *testing.T) {
	for _, th := range []string{"system", "light", "dark"} {
		got, ok := ParseTheme(th)
		assert.True(t, ok, th)
		assert.Equal(t, Theme(th), got)
	}
	for _, bad := range []string{"", "Dark", "sepia", " light"} {
		_, ok := ParseTheme(bad)
		assert.False(t, ok, bad)
	}
	for _, c := range []string{"BRL", "USD", "EUR"} {
		got, ok := ParseCurrency(c)
		assert.True(t, ok, c)
		assert.Equal(t, c, got)
	}
	for _, bad := range []string{"", "brl", "GBP", "BRL "} {
		_, ok := ParseCurrency(bad)
		assert.False(t, ok, bad)
	}
}

func TestPreferencesPatch(t *testing.T) {
	current := DefaultPreferences(i18n.PtBR)

	assert.True(t, PreferencesPatch{}.Empty())
	same, err := PreferencesPatch{}.Apply(current)
	require.NoError(t, err)
	assert.Equal(t, current, same)

	patch := PreferencesPatch{Locale: ptr("en-US"), Theme: ptr("dark")}
	assert.False(t, patch.Empty())
	next, err := patch.Apply(current)
	require.NoError(t, err)
	assert.Equal(t, Preferences{Locale: i18n.EnUS, Theme: ThemeDark, Currency: CurrencyBRL}, next)

	next, err = PreferencesPatch{Currency: ptr("EUR")}.Apply(next)
	require.NoError(t, err)
	assert.Equal(t, Preferences{Locale: i18n.EnUS, Theme: ThemeDark, Currency: CurrencyEUR}, next)

	_, err = PreferencesPatch{Locale: ptr("fr-FR"), Theme: ptr("sepia"), Currency: ptr("GBP")}.Apply(current)
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, []FieldError{{"locale", CodeInvalid}, {"theme", CodeInvalid}, {"currency", CodeInvalid}}, ve.Fields)

	_, err = PreferencesPatch{Locale: ptr("en-US"), Theme: ptr("")}.Apply(current)
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, []FieldError{{"theme", CodeInvalid}}, ve.Fields, "one bad field rejects the whole patch")
}

func ptr[T any](v T) *T { return &v }
