package auth

import (
	"crypto/subtle"
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// TOTP parameters (RFC 6238). SHA-1, 6 digits and 30 seconds are what every
// authenticator app supports; the 160-bit secret follows RFC 4226 §4.
const (
	TOTPPeriod      = 30 * time.Second
	totpPeriodSecs  = 30
	totpSecretBytes = 20
	totpDigits      = 6
	// totpSkew accepts the code of one period before and after the current
	// one, for clock drift and typing time.
	totpSkew = 1
)

var totpOpts = totp.ValidateOpts{Period: totpPeriodSecs, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

// TOTPKey is a new TOTP secret (base32) and the otpauth:// URI that
// authenticator apps import, usually from a QR code.
type TOTPKey struct {
	Secret string
	URI    string
}

// NewTOTPKey generates a random secret for account (the user's email) under issuer.
func NewTOTPKey(issuer, account string) (TOTPKey, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: account,
		Period:      totpPeriodSecs,
		SecretSize:  totpSecretBytes,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return TOTPKey{}, fmt.Errorf("generate totp key: %w", err)
	}
	return TOTPKey{Secret: key.Secret(), URI: key.URL()}, nil
}

// LooksLikeTOTPCode reports whether input, once spaces are removed, is six
// digits. It tells TOTP codes from recovery codes.
func LooksLikeTOTPCode(input string) bool {
	_, ok := normalizeTOTPCode(input)
	return ok
}

func normalizeTOTPCode(input string) (string, bool) {
	code := strings.ReplaceAll(strings.TrimSpace(input), " ", "")
	if len(code) != totpDigits {
		return "", false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return code, true
}

// MatchTOTP checks code against secret at now, within one period of drift
// each way. It returns the time step the code belongs to, so the caller can
// refuse a step that was already used (RFC 6238 §5.2): without that, a code
// seen over someone's shoulder works again for up to 90 seconds. Every
// candidate step is computed and compared in constant time.
func MatchTOTP(secret, input string, now time.Time) (step int64, ok bool, err error) {
	code, valid := normalizeTOTPCode(input)
	if !valid {
		return 0, false, nil
	}
	current := now.Unix() / totpPeriodSecs
	for s := current - totpSkew; s <= current+totpSkew; s++ {
		want, err := totp.GenerateCodeCustom(secret, time.Unix(s*totpPeriodSecs, 0), totpOpts)
		if err != nil {
			return 0, false, fmt.Errorf("generate totp code: %w", err)
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			// On the rare collision between steps, the latest one wins: it
			// is the stricter one for replay checks.
			step, ok = s, true
		}
	}
	return step, ok, nil
}
