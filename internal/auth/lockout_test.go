package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLockForDoublesUpToTheCap(t *testing.T) {
	p := DefaultLockoutPolicy
	for failures, want := range map[int]time.Duration{
		0: 0, 4: 0,
		5: time.Minute, 6: 2 * time.Minute, 7: 4 * time.Minute, 8: 8 * time.Minute,
		9: 15 * time.Minute, 10: 15 * time.Minute, 1000: 15 * time.Minute,
	} {
		assert.Equal(t, want, p.LockFor(failures, p.EmailLockAfter), "%d failures", failures)
	}
	assert.Equal(t, time.Duration(0), p.LockFor(19, p.IPLockAfter))
	assert.Equal(t, time.Minute, p.LockFor(20, p.IPLockAfter))

	odd := LockoutPolicy{BaseLock: 40 * time.Second, MaxLock: time.Minute}
	assert.Equal(t, 40*time.Second, odd.LockFor(1, 1))
	assert.Equal(t, time.Minute, odd.LockFor(2, 1))
}

func TestNeedsCaptcha(t *testing.T) {
	p := DefaultLockoutPolicy
	assert.False(t, p.NeedsCaptcha(LoginState{EmailFailures: 2, IPFailures: 9}))
	assert.True(t, p.NeedsCaptcha(LoginState{EmailFailures: 3}))
	assert.True(t, p.NeedsCaptcha(LoginState{IPFailures: 10}))
}
