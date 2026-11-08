package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/audit"
	core "github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/email"
)

func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	require.NoError(t, err)
	return code
}

func (h *harness) principal() core.Principal {
	return core.Principal{UserID: h.users.only().ID, SessionID: "s-current"}
}

// mfaUser is a verified account with MFA on. It returns the TOTP secret and
// the recovery codes, with the clock moved past the step used to enable it.
func (h *harness) mfaUser(t *testing.T) (secret string, codes []string) {
	t.Helper()
	h.verifiedUser(t)
	setup, err := h.SetupMFA(ctx, h.principal(), password, client)
	require.NoError(t, err)
	codes, err = h.EnableMFA(ctx, h.principal(), codeAt(t, setup.Secret, h.clock.now()), client)
	require.NoError(t, err)
	h.clock.advance(30 * time.Second)
	return setup.Secret, codes
}

// challenge logs in with the password and returns the mfaToken.
func (h *harness) challenge(t *testing.T) string {
	t.Helper()
	res, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	require.NoError(t, err)
	require.NotNil(t, res.Challenge)
	return res.Challenge.Token
}

func TestSetupMFARequiresThePassword(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)

	for _, pw := range []string{"", "wrong password!", strings.Repeat("x", maxPasswordBytes+1)} {
		_, err := h.SetupMFA(ctx, h.principal(), pw, client)
		assert.ErrorIs(t, err, ErrPasswordIncorrect)
	}
	assert.Equal(t, 1, h.lockout.fails["e:"+core.HashIdentifier("email", "ana@example.com")], "a wrong password counts toward the lockout")
	assert.Equal(t, 1, countEvents(h.audit, audit.EventReauthFailure))
	assert.Empty(t, h.users.only().MFA.PendingSecret)

	setup, err := h.SetupMFA(ctx, h.principal(), password, client)
	require.NoError(t, err)
	assert.Len(t, setup.Secret, 32)
	assert.True(t, strings.HasPrefix(setup.URI, "otpauth://totp/"), setup.URI)
	assert.Contains(t, setup.URI, "issuer=Monthra%20%28test%29")
	assert.Contains(t, setup.URI, "ana@example.com")
	assert.Equal(t, h.clock.now().Add(MFASetupTTL), setup.ExpiresAt)

	got := h.users.byID[u.ID]
	assert.True(t, strings.HasPrefix(got.MFA.PendingSecret, "e1:"), "sealed at rest")
	assert.NotContains(t, got.MFA.PendingSecret, setup.Secret)
	assert.False(t, got.MFAEnabled(), "nothing changes until a code confirms it")
	assert.Equal(t, 1, countEvents(h.audit, audit.EventMFASetup))
}

func TestSetupMFAIsLockedWithTheLogin(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	for range core.DefaultLockoutPolicy.EmailLockAfter {
		_, err := h.SetupMFA(ctx, h.principal(), "wrong password!", client)
		require.ErrorIs(t, err, ErrPasswordIncorrect)
	}
	var limited *RateLimitedError
	_, err := h.SetupMFA(ctx, h.principal(), password, client)
	assert.ErrorAs(t, err, &limited, "the address is locked, the right password included")
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorAs(t, err, &limited, "and so is the login")
}

func TestEnableMFA(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)
	_, err := h.EnableMFA(ctx, h.principal(), "123456", client)
	assert.ErrorIs(t, err, ErrMFASetupRequired)

	setup, err := h.SetupMFA(ctx, h.principal(), password, client)
	require.NoError(t, err)
	_, err = h.EnableMFA(ctx, h.principal(), "000000", client)
	assert.ErrorIs(t, err, ErrMFACodeInvalid)
	_, err = h.EnableMFA(ctx, h.principal(), codeAt(t, setup.Secret, h.clock.now().Add(-2*time.Minute)), client)
	assert.ErrorIs(t, err, ErrMFACodeInvalid, "old codes are refused")

	codes, err := h.EnableMFA(ctx, h.principal(), codeAt(t, setup.Secret, h.clock.now()), client)
	require.NoError(t, err)
	require.Len(t, codes, core.RecoveryCodeCount)
	seen := map[string]bool{}
	for _, c := range codes {
		assert.False(t, seen[c])
		seen[c] = true
	}
	got := h.users.byID[u.ID]
	require.True(t, got.MFAEnabled())
	assert.Equal(t, h.clock.now().Unix()/30, got.MFA.LastStep, "the confirming code is spent")
	assert.Len(t, got.MFA.RecoveryCodes, core.RecoveryCodeCount)
	for _, hash := range got.MFA.RecoveryCodes {
		assert.NotContains(t, codes, hash, "stored as hashes")
	}
	assert.Empty(t, got.MFA.PendingSecret)
	assert.Equal(t, 1, countEvents(h.audit, audit.EventMFAEnabled))
	assert.Equal(t, 1, h.metrics.mfa["enabled"])
	notice := h.mail.sent[len(h.mail.sent)-1]
	assert.Equal(t, email.TemplateMFAEnabled, notice.Template)
	assert.Equal(t, "ana@example.com", notice.To)

	_, err = h.EnableMFA(ctx, h.principal(), codeAt(t, setup.Secret, h.clock.now()), client)
	assert.ErrorIs(t, err, ErrMFAAlreadyEnabled)
	_, err = h.SetupMFA(ctx, h.principal(), password, client)
	assert.ErrorIs(t, err, ErrMFAAlreadyEnabled)
}

func TestEnableMFAAfterTheSetupExpired(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	setup, err := h.SetupMFA(ctx, h.principal(), password, client)
	require.NoError(t, err)
	h.clock.advance(MFASetupTTL)
	_, err = h.EnableMFA(ctx, h.principal(), codeAt(t, setup.Secret, h.clock.now()), client)
	assert.ErrorIs(t, err, ErrMFASetupRequired)
}

func TestLoginWithMFAAsksForTheSecondFactor(t *testing.T) {
	h := newHarness()
	h.mfaUser(t)
	for range 2 {
		_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: "wrong password!"}, client)
		require.ErrorIs(t, err, ErrInvalidCredentials)
	}
	resets := h.lockout.resets

	res, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	require.NoError(t, err)
	require.NotNil(t, res.Challenge)
	assert.True(t, core.ValidOpaqueToken(res.Challenge.Token))
	assert.Equal(t, h.clock.now().Add(core.MFAChallengeTTL), res.Challenge.ExpiresAt)
	assert.Empty(t, res.Tokens.AccessToken)
	assert.Empty(t, h.sessions.byID, "no session before the second factor")
	assert.Equal(t, 1, h.metrics.logins["mfa_required"])
	assert.Equal(t, resets, h.lockout.resets, "failures are kept until the second factor passes")
	assert.Equal(t, 2, h.lockout.fails["e:"+core.HashIdentifier("email", "ana@example.com")])
	stored, ok := h.challenges.byHash[core.HashToken(res.Challenge.Token)]
	require.True(t, ok, "stored by hash")
	assert.Equal(t, core.HashIdentifier("email", "ana@example.com"), stored.EmailKey)
	assert.Equal(t, core.MFAChallengeTTL, h.challenges.ttls[core.HashToken(res.Challenge.Token)])
}

func TestVerifyMFAOpensASession(t *testing.T) {
	h := newHarness()
	secret, _ := h.mfaUser(t)
	_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: "wrong password!"}, client)
	require.ErrorIs(t, err, ErrInvalidCredentials)
	token := h.challenge(t)

	tokens, err := h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: codeAt(t, secret, h.clock.now())}, client)
	require.NoError(t, err)
	assert.NotEmpty(t, tokens.AccessToken)
	assert.True(t, core.ValidOpaqueToken(tokens.RefreshToken))
	assert.Len(t, h.sessions.byID, 1)
	last := h.audit.entries[len(h.audit.entries)-1]
	assert.Equal(t, audit.EventLoginSuccess, last.Event)
	assert.Equal(t, "totp", last.Reason)
	assert.Zero(t, h.lockout.fails["e:"+core.HashIdentifier("email", "ana@example.com")], "a full login forgets the failures")
	assert.Zero(t, h.challenges.count(), "the challenge is spent")
	assert.Equal(t, 1, h.metrics.logins["success"])

	_, err = h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: codeAt(t, secret, h.clock.now())}, client)
	assert.ErrorIs(t, err, ErrMFAChallengeInvalid)
}

func TestVerifyMFARefusesReplayedCodes(t *testing.T) {
	h := newHarness()
	secret, _ := h.mfaUser(t)
	code := codeAt(t, secret, h.clock.now())
	_, err := h.VerifyMFA(ctx, VerifyMFAInput{Token: h.challenge(t), Code: code}, client)
	require.NoError(t, err)

	token := h.challenge(t)
	_, err = h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: code}, client)
	assert.ErrorIs(t, err, ErrMFACodeInvalid, "a code works once")
	assert.Equal(t, "replayed_code", h.audit.entries[len(h.audit.entries)-1].Reason)
	_, err = h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: codeAt(t, secret, h.clock.now().Add(-30*time.Second))}, client)
	assert.ErrorIs(t, err, ErrMFACodeInvalid, "and so does any older step")

	h.clock.advance(30 * time.Second)
	_, err = h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: codeAt(t, secret, h.clock.now())}, client)
	assert.NoError(t, err, "the next step works")
}

func TestVerifyMFAWithARecoveryCode(t *testing.T) {
	h := newHarness()
	_, codes := h.mfaUser(t)
	typed := " " + strings.ToUpper(strings.ReplaceAll(codes[3], "-", " ")) + " "

	_, err := h.VerifyMFA(ctx, VerifyMFAInput{Token: h.challenge(t), Code: typed}, client)
	require.NoError(t, err)
	assert.Equal(t, 1, countEvents(h.audit, audit.EventMFARecoveryCodeUsed))
	assert.Equal(t, 1, h.metrics.mfa["recovery_code_used"])
	assert.Len(t, h.users.only().MFA.RecoveryCodes, core.RecoveryCodeCount-1)

	_, err = h.VerifyMFA(ctx, VerifyMFAInput{Token: h.challenge(t), Code: codes[3]}, client)
	assert.ErrorIs(t, err, ErrMFACodeInvalid, "each recovery code works once")
	_, err = h.VerifyMFA(ctx, VerifyMFAInput{Token: h.challenge(t), Code: "not a code"}, client)
	assert.ErrorIs(t, err, ErrMFACodeInvalid)
}

func TestVerifyMFAAttemptsAreLimited(t *testing.T) {
	h := newHarness()
	secret, _ := h.mfaUser(t)
	token := h.challenge(t)
	for range core.MFAMaxAttempts {
		_, err := h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: "000000"}, client)
		require.ErrorIs(t, err, ErrMFACodeInvalid)
	}
	assert.Equal(t, core.MFAMaxAttempts, countEvents(h.audit, audit.EventMFAFailure))
	assert.Equal(t, core.MFAMaxAttempts, h.metrics.logins["mfa_failure"])
	assert.Zero(t, h.challenges.count(), "the last allowed attempt drops the challenge")
	_, err := h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: codeAt(t, secret, h.clock.now())}, client)
	assert.ErrorIs(t, err, ErrMFAChallengeInvalid)
}

func TestVerifyMFAChallengeOverItsAttempts(t *testing.T) {
	h := newHarness()
	secret, _ := h.mfaUser(t)
	token := h.challenge(t)
	h.challenges.byHash[core.HashToken(token)] = core.MFAChallenge{UserID: h.users.only().ID, Attempts: core.MFAMaxAttempts}
	_, err := h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: codeAt(t, secret, h.clock.now())}, client)
	assert.ErrorIs(t, err, ErrMFAChallengeInvalid)
	assert.Zero(t, h.challenges.count())
}

func TestWrongCodesLockTheAddress(t *testing.T) {
	h := newHarness()
	secret, _ := h.mfaUser(t)
	first := h.challenge(t)
	for range 4 {
		_, err := h.VerifyMFA(ctx, VerifyMFAInput{Token: first, Code: "000000"}, client)
		require.ErrorIs(t, err, ErrMFACodeInvalid)
	}
	second := h.challenge(t) // the password alone does not clear the failures
	_, err := h.VerifyMFA(ctx, VerifyMFAInput{Token: second, Code: "000000"}, client)
	require.ErrorIs(t, err, ErrMFACodeInvalid)

	var limited *RateLimitedError
	_, err = h.VerifyMFA(ctx, VerifyMFAInput{Token: second, Code: codeAt(t, secret, h.clock.now())}, client)
	require.ErrorAs(t, err, &limited, "five wrong codes lock the address")
	assert.Equal(t, time.Minute, limited.RetryAfter)
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorAs(t, err, &limited)
}

func TestVerifyMFARejectsBadChallenges(t *testing.T) {
	h := newHarness()
	secret, _ := h.mfaUser(t)
	for _, token := range []string{"", "short", core.NewOpaqueToken()} {
		_, err := h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: codeAt(t, secret, h.clock.now())}, client)
		assert.ErrorIs(t, err, ErrMFAChallengeInvalid, "%q", token)
	}

	token := h.challenge(t)
	u := h.users.only()
	u.MFA = domain.MFA{}
	h.users.byID[u.ID] = u
	_, err := h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: "000000"}, client)
	assert.ErrorIs(t, err, ErrMFAChallengeInvalid, "MFA was turned off meanwhile")
	assert.Zero(t, h.challenges.count())

	h = newHarness()
	h.mfaUser(t)
	token = h.challenge(t)
	delete(h.users.byID, h.users.only().ID)
	_, err = h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: "000000"}, client)
	assert.ErrorIs(t, err, ErrMFAChallengeInvalid, "the account is gone")
}

func TestDisableMFA(t *testing.T) {
	h := newHarness()
	secret, codes := h.mfaUser(t)
	p := h.principal()

	assert.ErrorIs(t, h.DisableMFA(ctx, p, DisableMFAInput{Password: "wrong password!", Code: codeAt(t, secret, h.clock.now())}, client), ErrPasswordIncorrect)
	assert.ErrorIs(t, h.DisableMFA(ctx, p, DisableMFAInput{Password: password, Code: "000000"}, client), ErrMFACodeInvalid)
	assert.Equal(t, 2, h.lockout.fails["e:"+core.HashIdentifier("email", "ana@example.com")], "both failures count")
	assert.True(t, h.users.only().MFAEnabled())

	require.NoError(t, h.DisableMFA(ctx, p, DisableMFAInput{Password: password, Code: codes[0]}, client), "a recovery code also works")
	got := h.users.only()
	assert.False(t, got.MFAEnabled())
	assert.Empty(t, got.MFA.Secret)
	assert.Empty(t, got.MFA.RecoveryCodes)
	assert.Equal(t, 1, countEvents(h.audit, audit.EventMFADisabled))
	assert.Equal(t, 1, h.metrics.mfa["disabled"])
	assert.Equal(t, email.TemplateMFADisabled, h.mail.sent[len(h.mail.sent)-1].Template)

	assert.ErrorIs(t, h.DisableMFA(ctx, p, DisableMFAInput{Password: password, Code: codes[1]}, client), ErrMFANotEnabled)
	res, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	require.NoError(t, err)
	assert.Nil(t, res.Challenge, "the password is enough again")
}

func TestDisableMFAWithATOTPCode(t *testing.T) {
	h := newHarness()
	secret, _ := h.mfaUser(t)
	require.NoError(t, h.DisableMFA(ctx, h.principal(), DisableMFAInput{Password: password, Code: codeAt(t, secret, h.clock.now())}, client))
	assert.False(t, h.users.only().MFAEnabled())
}

func TestMFASecretsAreResealedAfterAKeyRotation(t *testing.T) {
	h := newHarness()
	secret, _ := h.mfaUser(t)
	require.True(t, strings.HasPrefix(h.users.only().MFA.Secret, "e1:"))

	h.Sealer = newSealer("e2")
	_, err := h.VerifyMFA(ctx, VerifyMFAInput{Token: h.challenge(t), Code: codeAt(t, secret, h.clock.now())}, client)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(h.users.only().MFA.Secret, "e2:"), "sealed again under the active key")

	h.clock.advance(30 * time.Second)
	_, err = h.VerifyMFA(ctx, VerifyMFAInput{Token: h.challenge(t), Code: codeAt(t, secret, h.clock.now())}, client)
	assert.NoError(t, err)
}

func TestMFAReportsStoreFailures(t *testing.T) {
	h := newHarness()
	secret, _ := h.mfaUser(t)
	token := h.challenge(t)
	h.challenges.err = errDown
	_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorIs(t, err, ErrUnavailable, "challenges fail closed")
	_, err = h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: codeAt(t, secret, h.clock.now())}, client)
	assert.ErrorIs(t, err, ErrUnavailable)

	h = newHarness()
	secret, _ = h.mfaUser(t)
	token = h.challenge(t)
	h.Sealer = mustSealer(t, map[string][]byte{"e9": testKeys["e2"]}, "e9")
	_, err = h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: codeAt(t, secret, h.clock.now())}, client)
	assert.ErrorContains(t, err, "open mfa secret", "a secret the ring cannot open is an internal error")

	h = newHarness()
	h.mfaUser(t)
	token = h.challenge(t)
	h.lockout.err = errDown
	_, err = h.VerifyMFA(ctx, VerifyMFAInput{Token: token, Code: "000000"}, client)
	assert.ErrorIs(t, err, ErrUnavailable)
	_, err = h.SetupMFA(ctx, h.principal(), password, client)
	assert.ErrorIs(t, err, ErrMFAAlreadyEnabled, "state is checked first")
	assert.ErrorIs(t, h.DisableMFA(ctx, h.principal(), DisableMFAInput{Password: password, Code: "000000"}, client), ErrUnavailable)

	h = newHarness()
	h.verifiedUser(t)
	p := h.principal()
	h.users.err = errDown
	_, err = h.SetupMFA(ctx, p, password, client)
	assert.ErrorIs(t, err, errDown)
	_, err = h.EnableMFA(ctx, p, "123456", client)
	assert.ErrorIs(t, err, errDown)
	assert.ErrorIs(t, h.DisableMFA(ctx, p, DisableMFAInput{}, client), errDown)

	h = newHarness()
	_, err = h.SetupMFA(ctx, core.Principal{UserID: "gone"}, password, client)
	assert.ErrorIs(t, err, ErrUnauthorized, "a deleted account ends the session")
}
