package domain

import (
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// Limits of the user fields (§6.1: at least 10 characters, no composition rules).
const (
	MaxEmailLength     = 254
	maxEmailLocalPart  = 64
	MinPasswordLength  = 10
	MaxPasswordLength  = 1024
	MaxNameLength      = 100
	DefaultCurrency    = CurrencyBRL
	DefaultTheme       = ThemeSystem
	maxPasswordBytes   = 4 * MaxPasswordLength // 1024 runes of at most 4 bytes
	minEmailAddressLen = 3                     // a@b
)

// Theme is the app color scheme.
type Theme string

// Supported themes.
const (
	ThemeSystem Theme = "system"
	ThemeLight  Theme = "light"
	ThemeDark   Theme = "dark"
)

// Themes lists the supported themes.
var Themes = []Theme{ThemeSystem, ThemeLight, ThemeDark}

// ParseTheme returns the theme named s, if it is supported.
func ParseTheme(s string) (Theme, bool) {
	for _, t := range Themes {
		if string(t) == s {
			return t, true
		}
	}
	return "", false
}

// Supported currencies (ISO 4217).
const (
	CurrencyBRL = "BRL"
	CurrencyUSD = "USD"
	CurrencyEUR = "EUR"
)

// Currencies lists the supported display currencies.
var Currencies = []string{CurrencyBRL, CurrencyUSD, CurrencyEUR}

// ParseCurrency returns the currency code s, if it is supported. Codes are
// upper case, as ISO 4217 writes them.
func ParseCurrency(s string) (string, bool) {
	for _, c := range Currencies {
		if c == s {
			return c, true
		}
	}
	return "", false
}

// Preferences are the profile settings (§7.6).
type Preferences struct {
	Locale   i18n.Locale
	Theme    Theme
	Currency string
}

// DefaultPreferences returns the preferences of a new account. The locale
// comes from the request that created it.
func DefaultPreferences(locale i18n.Locale) Preferences {
	if _, ok := i18n.Parse(string(locale)); !ok {
		locale = i18n.Default
	}
	return Preferences{Locale: locale, Theme: DefaultTheme, Currency: DefaultCurrency}
}

// PreferencesPatch changes some preferences; nil fields stay as they are.
type PreferencesPatch struct {
	Locale   *string
	Theme    *string
	Currency *string
}

// Empty reports whether the patch changes nothing.
func (p PreferencesPatch) Empty() bool {
	return p.Locale == nil && p.Theme == nil && p.Currency == nil
}

// Apply checks every field of the patch against the supported values and
// returns current with the changes, or a *ValidationError naming each
// unsupported field.
func (p PreferencesPatch) Apply(current Preferences) (Preferences, error) {
	var v Validation
	next := current
	if p.Locale != nil {
		if l, ok := i18n.Parse(*p.Locale); ok {
			next.Locale = l
		} else {
			v.Add("locale", CodeInvalid)
		}
	}
	if p.Theme != nil {
		if t, ok := ParseTheme(*p.Theme); ok {
			next.Theme = t
		} else {
			v.Add("theme", CodeInvalid)
		}
	}
	if p.Currency != nil {
		if c, ok := ParseCurrency(*p.Currency); ok {
			next.Currency = c
		} else {
			v.Add("currency", CodeInvalid)
		}
	}
	if err := v.Err(); err != nil {
		return Preferences{}, err
	}
	return next, nil
}

// User is an account holder.
type User struct {
	ID              string
	Name            string
	Email           string
	PasswordHash    string
	EmailVerifiedAt *time.Time
	Preferences     Preferences
	MFA             MFA
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// EmailVerified reports whether the owner confirmed the email address.
func (u User) EmailVerified() bool { return u.EmailVerifiedAt != nil }

// MFAEnabled reports whether sign-in asks for a TOTP or recovery code.
func (u User) MFAEnabled() bool { return u.MFA.EnabledAt != nil }

// MFA is the two-step verification state of an account (§6.1). Secrets are
// sealed with AES-256-GCM (§6.6) and recovery codes are stored as hashes:
// nothing here signs in on its own if the database leaks.
type MFA struct {
	// Secret is the sealed TOTP secret, set while MFA is enabled.
	Secret    string
	EnabledAt *time.Time
	// PendingSecret is a sealed secret waiting for its first code, until PendingExpiresAt.
	PendingSecret    string
	PendingExpiresAt *time.Time
	// RecoveryCodes are the hashes of the unused recovery codes.
	RecoveryCodes []string
	// LastStep is the TOTP time step of the last accepted code; older or
	// equal steps are refused, so a code works only once.
	LastStep int64
}

// PendingActive reports whether a setup is waiting for its first code at now.
func (m MFA) PendingActive(now time.Time) bool {
	return m.PendingSecret != "" && m.PendingExpiresAt != nil && now.Before(*m.PendingExpiresAt)
}

// NewUser builds an unverified account from already validated fields.
func NewUser(name, email, passwordHash string, locale i18n.Locale, now time.Time) User {
	now = now.UTC()
	return User{
		Name:         name,
		Email:        email,
		PasswordHash: passwordHash,
		Preferences:  DefaultPreferences(locale),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

// NormalizeEmail trims and lowercases an address and checks that it is a
// bare address (no display name, no comments) of a sane length.
func NormalizeEmail(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case s == "":
		return "", &fieldErr{CodeRequired}
	case len(s) > MaxEmailLength:
		return "", &fieldErr{CodeTooLong}
	case len(s) < minEmailAddressLen || strings.ContainsAny(s, " \t\r\n<>\"(),;:\\[]"):
		return "", &fieldErr{CodeInvalid}
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Name != "" || addr.Address != s {
		return "", &fieldErr{CodeInvalid}
	}
	local, domain, ok := strings.Cut(s, "@")
	if !ok || local == "" || len(local) > maxEmailLocalPart || domain == "" || strings.Contains(domain, "@") ||
		strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return "", &fieldErr{CodeInvalid}
	}
	return s, nil
}

// ValidatePassword applies the password policy: 10 to 1024 characters, any
// characters, no composition rules. Breach checks happen in the use case.
func ValidatePassword(password string) error {
	if password == "" {
		return &fieldErr{CodeRequired}
	}
	if len(password) > maxPasswordBytes || !utf8.ValidString(password) {
		return &fieldErr{CodeInvalid}
	}
	switch n := utf8.RuneCountInString(password); {
	case n < MinPasswordLength:
		return &fieldErr{CodeTooShort}
	case n > MaxPasswordLength:
		return &fieldErr{CodeTooLong}
	}
	return nil
}

// NormalizeName trims a display name and checks it is 1 to 100 printable characters.
func NormalizeName(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", &fieldErr{CodeRequired}
	}
	if !utf8.ValidString(s) {
		return "", &fieldErr{CodeInvalid}
	}
	if utf8.RuneCountInString(s) > MaxNameLength {
		return "", &fieldErr{CodeTooLong}
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", &fieldErr{CodeInvalid}
		}
	}
	return s, nil
}
