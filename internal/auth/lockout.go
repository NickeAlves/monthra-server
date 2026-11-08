package auth

import "time"

// LockoutPolicy sets when failed logins lock an email address or a client IP
// (§6.1: progressive lock by account and by IP, with backoff).
type LockoutPolicy struct {
	// EmailLockAfter failures lock the address. IPLockAfter is higher because
	// many people can share one IP (offices, carrier NAT).
	EmailLockAfter int
	IPLockAfter    int
	// CaptchaAfter failures, by address or IP, require a captcha (when enabled).
	CaptchaAfterEmail int
	CaptchaAfterIP    int
	// BaseLock is the first lock; each further failure doubles it, up to MaxLock.
	BaseLock time.Duration
	MaxLock  time.Duration
	// Window is how long failures are remembered after the last one.
	Window time.Duration
	// RegisterCaptchaAfter sign-ups from one IP within Window require a captcha.
	RegisterCaptchaAfter int
}

// DefaultLockoutPolicy locks an address after 5 failures for 1 minute,
// doubling up to 15 minutes.
var DefaultLockoutPolicy = LockoutPolicy{
	EmailLockAfter:       5,
	IPLockAfter:          20,
	CaptchaAfterEmail:    3,
	CaptchaAfterIP:       10,
	BaseLock:             time.Minute,
	MaxLock:              15 * time.Minute,
	Window:               time.Hour,
	RegisterCaptchaAfter: 3,
}

// LockFor is the lock that the failures-th failure starts, or zero below the threshold.
func (p LockoutPolicy) LockFor(failures, threshold int) time.Duration {
	if failures < threshold {
		return 0
	}
	d := p.BaseLock
	for range failures - threshold {
		d *= 2
		if d >= p.MaxLock {
			return p.MaxLock
		}
	}
	return min(d, p.MaxLock)
}

// LoginState is what the lockout knows about an address and an IP.
type LoginState struct {
	// RetryAfter is the remaining lock, zero when not locked.
	RetryAfter    time.Duration
	EmailFailures int
	IPFailures    int
}

// NeedsCaptcha reports whether p asks for a captcha in state s.
func (p LockoutPolicy) NeedsCaptcha(s LoginState) bool {
	return s.EmailFailures >= p.CaptchaAfterEmail || s.IPFailures >= p.CaptchaAfterIP
}
