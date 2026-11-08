package auth

import "time"

// MFAChallengeTTL is how long a login waits for its second factor.
const MFAChallengeTTL = 5 * time.Minute

// MFAMaxAttempts codes can be tried per challenge; the next try kills it.
const MFAMaxAttempts = 5

// MFAChallenge is a login waiting for its second factor: the password was
// right, and the mfaToken handed to the client points at this record.
type MFAChallenge struct {
	UserID string
	// EmailKey is the hashed address, the lockout subject of the login.
	EmailKey string
	// Attempts counts the codes tried so far, the current one included.
	Attempts int
}
