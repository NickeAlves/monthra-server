package auth

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// RecoveryCodeCount codes are issued when MFA is enabled (§6.1). Each one
// signs in once in place of a TOTP code.
const RecoveryCodeCount = 10

const (
	recoveryCodeBytes = 10 // 80 bits
	recoveryCodeLen   = 16 // base32 characters: exactly 80 bits, no padding or spare bits
	recoveryGroupLen  = 4
)

var recoveryEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewRecoveryCodes returns n random codes formatted for display, such as
// "abcd-efgh-ijkl-mnop". The base32 alphabet has no 0, 1, 8 or 9, so no
// character reads like another.
func NewRecoveryCodes(n int) []string {
	codes := make([]string, n)
	for i := range codes {
		b := make([]byte, recoveryCodeBytes)
		_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
		raw := strings.ToLower(recoveryEncoding.EncodeToString(b))
		groups := make([]string, 0, recoveryCodeLen/recoveryGroupLen)
		for j := 0; j < len(raw); j += recoveryGroupLen {
			groups = append(groups, raw[j:j+recoveryGroupLen])
		}
		codes[i] = strings.Join(groups, "-")
	}
	return codes
}

// NormalizeRecoveryCode returns the canonical form of user input (hyphens
// and spaces removed, upper case), or false when it cannot be a recovery code.
func NormalizeRecoveryCode(input string) (string, bool) {
	s := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(input)))
	if len(s) != recoveryCodeLen {
		return "", false
	}
	if _, err := recoveryEncoding.DecodeString(s); err != nil {
		return "", false
	}
	return s, true
}

// HashRecoveryCode is how recovery codes are stored and looked up. They
// carry 80 random bits, so a fast hash is enough, as for opaque tokens.
func HashRecoveryCode(canonical string) string { return HashToken("recovery|" + canonical) }
