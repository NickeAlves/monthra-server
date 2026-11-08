package auth

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/audit"
	core "github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/email"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

var (
	ctx      = context.Background()
	password = strings.Repeat("correct horse ", 2)
	client   = Client{IP: netip.MustParseAddr("198.51.100.7"), UserAgent: "Mozilla/5.0 test", Locale: i18n.EsES}
)

func registerInput() RegisterInput {
	return RegisterInput{Name: " Ana ", Email: " Ana@Example.com ", Password: password}
}

func fieldsOf(t *testing.T, err error) []domain.FieldError {
	t.Helper()
	var ve *domain.ValidationError
	require.ErrorAs(t, err, &ve)
	return ve.Fields
}

// tokenFromLink extracts the token from a verification link.
func tokenFromLink(t *testing.T, link string) string {
	t.Helper()
	_, token, ok := strings.Cut(link, "#token=")
	require.True(t, ok, link)
	return token
}

// verifiedUser registers and verifies ana@example.com.
func (h *harness) verifiedUser(t *testing.T) domain.User {
	t.Helper()
	require.NoError(t, h.Register(ctx, registerInput(), client))
	require.NoError(t, h.VerifyEmail(ctx, tokenFromLink(t, h.mail.sent[len(h.mail.sent)-1].Link), client))
	return h.users.only()
}

func (h *harness) login(t *testing.T) Tokens {
	t.Helper()
	tokens, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	require.NoError(t, err)
	return tokens
}

func TestRegisterCreatesAnUnverifiedAccountAndSendsTheLink(t *testing.T) {
	h := newHarness()
	require.NoError(t, h.Register(ctx, registerInput(), client))

	u := h.users.only()
	assert.Equal(t, "Ana", u.Name)
	assert.Equal(t, "ana@example.com", u.Email)
	assert.Equal(t, "v1:"+password, u.PasswordHash)
	assert.False(t, u.EmailVerified())
	assert.Equal(t, i18n.EsES, u.Preferences.Locale, "the account takes the request locale")

	require.Len(t, h.mail.sent, 1)
	msg := h.mail.sent[0]
	assert.Equal(t, email.TemplateVerifyEmail, msg.Template)
	assert.Equal(t, "ana@example.com", msg.To)
	assert.Equal(t, i18n.EsES, msg.Locale)
	assert.True(t, strings.HasPrefix(msg.Link, "https://app.monthra.example/verify-email#token="), msg.Link)
	assert.Equal(t, 30*time.Minute, msg.LinkTTL)

	token := tokenFromLink(t, msg.Link)
	assert.True(t, core.ValidOpaqueToken(token))
	stored, ok := h.authTokens.byHash[core.HashToken(token)]
	require.True(t, ok, "stored by hash only")
	assert.Equal(t, u.ID, stored.UserID)
	assert.Equal(t, h.clock.now().Add(30*time.Minute), stored.ExpiresAt)

	assert.Equal(t, []audit.Event{audit.EventRegister}, h.audit.events())
	assert.Equal(t, "198.51.100.7", h.audit.entries[0].IP)
}

func TestRegisterValidatesEveryField(t *testing.T) {
	h := newHarness()
	err := h.Register(ctx, RegisterInput{Name: "", Email: "nope", Password: "short"}, client)
	assert.Equal(t, []domain.FieldError{{Field: "name", Code: "required"}, {Field: "email", Code: "invalid"}, {Field: "password", Code: "too_short"}}, fieldsOf(t, err))
	assert.Empty(t, h.users.byID)
	assert.Empty(t, h.limiter.subjects, "invalid input spends no budget")
}

func TestRegisterOfATakenAddressLooksTheSame(t *testing.T) {
	h := newHarness()
	require.NoError(t, h.Register(ctx, registerInput(), client))
	before := h.users.only()

	other := registerInput()
	other.Password = strings.Repeat("attacker pw ", 2)
	require.NoError(t, h.Register(ctx, other, client), "same answer as a new address")

	assert.Equal(t, before, h.users.only(), "the existing account is untouched")
	assert.Equal(t, 2, h.hasher.hashes, "the password is hashed either way")
	require.Len(t, h.mail.sent, 2)
	assert.Equal(t, email.TemplateRegisterAttempt, h.mail.sent[1].Template)
	assert.Equal(t, "https://app.monthra.example/forgot-password", h.mail.sent[1].Link)
	assert.Equal(t, 1, h.authTokens.pending(), "no new verification link for the attempt")
}

func TestRegisterRejectsBreachedPasswords(t *testing.T) {
	h := newHarness()
	*h.breach = fakeBreach{breached: true}
	err := h.Register(ctx, registerInput(), client)
	assert.Equal(t, []domain.FieldError{{Field: "password", Code: "breached"}}, fieldsOf(t, err))
	assert.Empty(t, h.users.byID)

	*h.breach = fakeBreach{err: errors.New("hibp timeout")}
	assert.NoError(t, h.Register(ctx, registerInput(), client), "the breach check fails open")
}

func TestRegisterSpendsTheEmailBudget(t *testing.T) {
	h := newHarness()
	h.limiter.limit = 1
	require.NoError(t, h.Register(ctx, registerInput(), client))
	err := h.Register(ctx, registerInput(), client)
	var rl *RateLimitedError
	require.ErrorAs(t, err, &rl)
	assert.Equal(t, 42*time.Second, rl.RetryAfter)
	assert.Equal(t, 1, h.metrics.rateLimited[PolicyAuthByEmail])
	assert.Equal(t, core.HashIdentifier("email", "ana@example.com"), h.limiter.subjects[0], "keyed by a hash, never the address")

	h.limiter.err = errDown
	assert.ErrorIs(t, h.Register(ctx, registerInput(), client), ErrUnavailable, "fails closed")
}

func TestRegisterAsksForACaptchaAfterRepeatedSignUps(t *testing.T) {
	h := newHarness()
	h.captcha.enabled = true
	for i := range 3 {
		in := registerInput()
		in.Email = string(rune('a'+i)) + "@example.com"
		require.NoError(t, h.Register(ctx, in, client), "sign-up %d", i+1)
	}
	in := registerInput()
	in.Email = "d@example.com"
	assert.ErrorIs(t, h.Register(ctx, in, client), ErrCaptchaRequired)
	in.CaptchaToken = "robot"
	assert.ErrorIs(t, h.Register(ctx, in, client), ErrCaptchaInvalid)
	in.CaptchaToken = "human"
	assert.NoError(t, h.Register(ctx, in, client))

	h.captcha.err = errDown
	in.Email = "e@example.com"
	assert.ErrorIs(t, h.Register(ctx, in, client), ErrUnavailable)
	h.lockout.err = errDown
	assert.ErrorIs(t, h.Register(ctx, in, client), ErrUnavailable)
}

func TestRegisterReportsStoreFailures(t *testing.T) {
	h := newHarness()
	h.hasher.err = errDown
	assert.ErrorIs(t, h.Register(ctx, registerInput(), client), errDown)

	h = newHarness()
	h.users.err = errDown
	assert.ErrorIs(t, h.Register(ctx, registerInput(), client), errDown)

	h = newHarness()
	h.authTokens.err = errDown
	assert.ErrorIs(t, h.Register(ctx, registerInput(), client), errDown)
}

func TestVerifyEmailIsSingleUse(t *testing.T) {
	h := newHarness()
	require.NoError(t, h.Register(ctx, registerInput(), client))
	token := tokenFromLink(t, h.mail.sent[0].Link)

	h.clock.advance(time.Minute)
	require.NoError(t, h.VerifyEmail(ctx, token, client))
	u := h.users.only()
	require.True(t, u.EmailVerified())
	assert.Equal(t, h.clock.now(), *u.EmailVerifiedAt)
	assert.Equal(t, audit.EventEmailVerified, h.audit.entries[len(h.audit.entries)-1].Event)

	assert.ErrorIs(t, h.VerifyEmail(ctx, token, client), ErrTokenInvalid, "used")
	assert.ErrorIs(t, h.VerifyEmail(ctx, "short", client), ErrTokenInvalid, "malformed")
	assert.ErrorIs(t, h.VerifyEmail(ctx, core.NewOpaqueToken(), client), ErrTokenInvalid, "unknown")
}

func TestVerifyEmailExpires(t *testing.T) {
	h := newHarness()
	require.NoError(t, h.Register(ctx, registerInput(), client))
	h.clock.advance(31 * time.Minute)
	assert.ErrorIs(t, h.VerifyEmail(ctx, tokenFromLink(t, h.mail.sent[0].Link), client), ErrTokenInvalid)
}

func TestVerifyEmailOfADeletedAccount(t *testing.T) {
	h := newHarness()
	require.NoError(t, h.Register(ctx, registerInput(), client))
	h.users.byID = nil
	assert.ErrorIs(t, h.VerifyEmail(ctx, tokenFromLink(t, h.mail.sent[0].Link), client), ErrTokenInvalid)

	h.authTokens.err = errDown
	assert.ErrorIs(t, h.VerifyEmail(ctx, core.NewOpaqueToken(), client), errDown)
}

func TestResendVerification(t *testing.T) {
	h := newHarness()
	require.NoError(t, h.Register(ctx, registerInput(), client))
	first := tokenFromLink(t, h.mail.sent[0].Link)

	other := client
	other.Locale = i18n.EnUS
	require.NoError(t, h.ResendVerification(ctx, "ANA@example.com", other))
	require.Len(t, h.mail.sent, 2)
	assert.Equal(t, i18n.EsES, h.mail.sent[1].Locale, "the account locale wins")
	assert.ErrorIs(t, h.VerifyEmail(ctx, first, client), ErrTokenInvalid, "only the newest link works")
	require.NoError(t, h.VerifyEmail(ctx, tokenFromLink(t, h.mail.sent[1].Link), client))

	require.NoError(t, h.ResendVerification(ctx, "ana@example.com", client), "verified: nothing sent")
	require.NoError(t, h.ResendVerification(ctx, "nobody@example.com", client), "unknown: nothing sent")
	assert.Len(t, h.mail.sent, 2)

	err := h.ResendVerification(ctx, "not an email", client)
	assert.Equal(t, []domain.FieldError{{Field: "email", Code: "invalid"}}, fieldsOf(t, err))

	h.limiter.err = errDown
	assert.ErrorIs(t, h.ResendVerification(ctx, "ana@example.com", client), ErrUnavailable)
	h.limiter.err = nil
	h.users.err = errDown
	assert.ErrorIs(t, h.ResendVerification(ctx, "ana@example.com", client), errDown)
}

func TestLoginOpensASession(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)

	tokens, err := h.Login(ctx, LoginInput{Email: " ANA@example.com", Password: password}, client)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(tokens.AccessToken, "at."+u.ID+"."), tokens.AccessToken)
	assert.Equal(t, h.clock.now().Add(15*time.Minute), tokens.AccessExpiresAt)
	assert.True(t, core.ValidOpaqueToken(tokens.RefreshToken))
	assert.Equal(t, h.clock.now().Add(30*24*time.Hour), tokens.RefreshExpiresAt)

	require.Len(t, h.sessions.byID, 1)
	for _, s := range h.sessions.byID {
		assert.Equal(t, u.ID, s.UserID)
		assert.Equal(t, "Mozilla/5.0 test", s.UserAgent)
		assert.Equal(t, "198.51.100.7", s.IP)
		assert.Equal(t, tokens.RefreshExpiresAt, s.ExpiresAt)
	}
	stored, ok := h.refresh.byHash[core.HashToken(tokens.RefreshToken)]
	require.True(t, ok)
	assert.Equal(t, u.ID, stored.UserID)

	assert.Equal(t, 1, h.metrics.logins["success"])
	assert.Equal(t, audit.EventLoginSuccess, h.audit.entries[len(h.audit.entries)-1].Event)
	assert.Equal(t, 1, h.lockout.resets)
}

func TestLoginFailuresAreGeneric(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)

	_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password + "x"}, client)
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	last := h.audit.entries[len(h.audit.entries)-1]
	assert.Equal(t, audit.Entry{Event: audit.EventLoginFailure, UserID: u.ID, IP: "198.51.100.7", UserAgent: "Mozilla/5.0 test", Reason: "wrong_password"}, last)

	_, err = h.Login(ctx, LoginInput{Email: "nobody@example.com", Password: password}, client)
	assert.ErrorIs(t, err, ErrInvalidCredentials)
	assert.Equal(t, 1, h.hasher.dummies, "unknown addresses still pay for a hash")
	assert.Empty(t, h.audit.entries[len(h.audit.entries)-1].UserID)

	for name, in := range map[string]LoginInput{
		"bad email":      {Email: "nope", Password: password},
		"empty password": {Email: "ana@example.com"},
		"huge password":  {Email: "ana@example.com", Password: strings.Repeat("a", 5000)},
	} {
		_, err := h.Login(ctx, in, client)
		assert.ErrorIs(t, err, ErrInvalidCredentials, name)
	}
	assert.Equal(t, 5, h.metrics.logins["failure"])
	assert.Empty(t, h.sessions.byID)
}

func TestLoginRequiresAVerifiedEmail(t *testing.T) {
	h := newHarness()
	require.NoError(t, h.Register(ctx, registerInput(), client))

	_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorIs(t, err, ErrEmailNotVerified)
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: "wrong password!"}, client)
	assert.ErrorIs(t, err, ErrInvalidCredentials, "only the right password learns the account is unverified")
	assert.Equal(t, 1, h.metrics.logins["unverified"])
}

func TestLoginLocksOutAfterRepeatedFailures(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	for range 5 {
		_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: "wrong password!"}, client)
		require.ErrorIs(t, err, ErrInvalidCredentials)
	}
	hashesBefore := h.hasher.dummies
	_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	var rl *RateLimitedError
	require.ErrorAs(t, err, &rl, "even the right password waits")
	assert.Equal(t, time.Minute, rl.RetryAfter)
	assert.Equal(t, 1, h.metrics.logins["locked"])
	assert.Equal(t, hashesBefore, h.hasher.dummies)

	h.lockout.err = errDown
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorIs(t, err, ErrUnavailable, "fails closed")
}

func TestLoginAsksForACaptchaAfterFailures(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	h.captcha.enabled = true
	for range 3 {
		_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: "wrong password!"}, client)
		require.ErrorIs(t, err, ErrInvalidCredentials)
	}
	_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorIs(t, err, ErrCaptchaRequired)
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password, CaptchaToken: "robot"}, client)
	assert.ErrorIs(t, err, ErrCaptchaInvalid)
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password, CaptchaToken: "human"}, client)
	assert.NoError(t, err)
	assert.Equal(t, 2, h.metrics.logins["captcha"])
}

func TestLoginUpgradesOutdatedHashes(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)
	require.NoError(t, h.users.UpdatePasswordHash(ctx, u.ID, "v0:"+password, h.clock.now()))
	h.login(t)
	assert.Equal(t, "v1:"+password, h.users.only().PasswordHash)
}

func TestLoginReportsStoreFailures(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)

	h.limiter.err = errDown
	_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorIs(t, err, ErrUnavailable)
	h.limiter.err = nil

	require.NoError(t, h.users.UpdatePasswordHash(ctx, u.ID, "garbage", h.clock.now()))
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorIs(t, err, core.ErrMalformedHash)
	require.NoError(t, h.users.UpdatePasswordHash(ctx, u.ID, "v1:"+password, h.clock.now()))

	h.sessions.err = errDown
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorIs(t, err, errDown)
	h.sessions.err = nil

	h.refresh.err = errDown
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorIs(t, err, errDown)
	h.refresh.err = nil

	h.tokens.err = errDown
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorIs(t, err, errDown)
	h.tokens.err = nil

	h.users.err = errDown
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorIs(t, err, errDown)
}

func TestRefreshRotates(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	first := h.login(t)

	h.clock.advance(time.Hour)
	second, err := h.Refresh(ctx, first.RefreshToken, client)
	require.NoError(t, err)
	assert.NotEqual(t, first.RefreshToken, second.RefreshToken)
	assert.NotEqual(t, first.AccessToken, second.AccessToken)
	assert.Equal(t, h.clock.now().Add(30*24*time.Hour), second.RefreshExpiresAt, "the session slides")
	for _, s := range h.sessions.byID {
		assert.Equal(t, h.clock.now(), s.LastUsedAt)
		assert.Equal(t, second.RefreshExpiresAt, s.ExpiresAt)
	}
	assert.NotNil(t, h.refresh.byHash[core.HashToken(first.RefreshToken)].UsedAt)

	h.clock.advance(time.Hour)
	_, err = h.Refresh(ctx, second.RefreshToken, client)
	assert.NoError(t, err, "the new token works")
}

func TestRefreshWithinTheGraceWindowConflicts(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	first := h.login(t)
	_, err := h.Refresh(ctx, first.RefreshToken, client)
	require.NoError(t, err)

	h.clock.advance(5 * time.Second)
	_, err = h.Refresh(ctx, first.RefreshToken, client)
	assert.ErrorIs(t, err, ErrRefreshConflict, "another tab rotated it a moment ago")
	assert.Empty(t, h.revocations.sessions, "nothing revoked")
	assert.Zero(t, h.metrics.reuse)
}

func TestRefreshReuseRevokesTheSession(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	first := h.login(t)
	second, err := h.Refresh(ctx, first.RefreshToken, client)
	require.NoError(t, err)

	h.clock.advance(time.Minute)
	_, err = h.Refresh(ctx, first.RefreshToken, client)
	assert.ErrorIs(t, err, ErrUnauthorized)
	assert.Equal(t, 1, h.metrics.reuse)
	assert.Equal(t, audit.EventRefreshReuseDetected, h.audit.entries[len(h.audit.entries)-1].Event)
	require.Len(t, h.revocations.sessions, 1)
	for sid, ttl := range h.revocations.sessions {
		assert.Equal(t, 15*time.Minute+core.ClockLeeway, ttl, "outlives every access token of the session")
		assert.NotNil(t, h.sessions.byID[sid].RevokedAt)
	}

	_, err = h.Refresh(ctx, second.RefreshToken, client)
	assert.ErrorIs(t, err, ErrUnauthorized, "the thief's (or victim's) newer token dies with the session")

	h.clock.advance(time.Second)
	_, err = h.Refresh(ctx, second.RefreshToken, client)
	assert.ErrorIs(t, err, ErrUnauthorized, "within the grace window of a revoked session")

	h.clock.advance(time.Minute)
	_, err = h.Refresh(ctx, first.RefreshToken, client)
	assert.ErrorIs(t, err, ErrUnauthorized)
	assert.Equal(t, 1, h.metrics.reuse, "replays against a revoked session are not counted again")
	assert.Equal(t, 1, countEvents(h.audit, audit.EventRefreshReuseDetected))
}

func countEvents(a *auditTrail, event audit.Event) int {
	n := 0
	for _, e := range a.entries {
		if e.Event == event {
			n++
		}
	}
	return n
}

func TestRefreshReuseOfAMissingSession(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	first := h.login(t)
	_, err := h.Refresh(ctx, first.RefreshToken, client)
	require.NoError(t, err)
	h.sessions.byID = nil
	_, err = h.Refresh(ctx, first.RefreshToken, client)
	assert.ErrorIs(t, err, ErrUnauthorized)
	assert.Zero(t, h.metrics.reuse)
}

func TestRefreshWithoutAGraceWindow(t *testing.T) {
	h := newHarness()
	h.o.RefreshGrace = 0
	h.verifiedUser(t)
	first := h.login(t)
	_, err := h.Refresh(ctx, first.RefreshToken, client)
	require.NoError(t, err)
	_, err = h.Refresh(ctx, first.RefreshToken, client)
	assert.ErrorIs(t, err, ErrUnauthorized, "any second use is reuse")
	assert.Equal(t, 1, h.metrics.reuse)

	assert.Zero(t, NewService(Deps{}, Options{RefreshGrace: -1}).o.RefreshGrace)
	assert.Equal(t, DefaultRefreshGrace, NewService(Deps{}, Options{}).o.RefreshGrace)
}

func TestRefreshRejectsBadTokens(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	tokens := h.login(t)

	_, err := h.Refresh(ctx, "short", client)
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = h.Refresh(ctx, core.NewOpaqueToken(), client)
	assert.ErrorIs(t, err, ErrUnauthorized, "unknown")

	h.clock.advance(31 * 24 * time.Hour)
	_, err = h.Refresh(ctx, tokens.RefreshToken, client)
	assert.ErrorIs(t, err, ErrUnauthorized, "expired")
	assert.Zero(t, h.metrics.reuse, "an expired token is not reuse")
}

func TestRefreshOfARevokedOrMissingSession(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	tokens := h.login(t)
	for id := range h.sessions.byID {
		require.NoError(t, h.sessions.Revoke(ctx, id, h.clock.now()))
	}
	_, err := h.Refresh(ctx, tokens.RefreshToken, client)
	assert.ErrorIs(t, err, ErrUnauthorized)

	tokens = h.login(t)
	h.sessions.byID = nil
	_, err = h.Refresh(ctx, tokens.RefreshToken, client)
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestRefreshReportsStoreFailures(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	tokens := h.login(t)
	h.sessions.err = errDown
	_, err := h.Refresh(ctx, tokens.RefreshToken, client)
	assert.ErrorIs(t, err, errDown)

	h = newHarness()
	h.verifiedUser(t)
	first := h.login(t)
	_, err = h.Refresh(ctx, first.RefreshToken, client)
	require.NoError(t, err)
	h.clock.advance(time.Minute)
	h.revocations.err = errDown
	_, err = h.Refresh(ctx, first.RefreshToken, client)
	assert.ErrorIs(t, err, ErrUnavailable, "a reuse that cannot be revoked fails closed")
}

func TestLogoutRevokesTheSessionAndToken(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)
	tokens := h.login(t)
	var sid string
	for id := range h.sessions.byID {
		sid = id
	}

	h.clock.advance(5 * time.Minute)
	p := core.Principal{UserID: u.ID, SessionID: sid, TokenID: "jti-1", ExpiresAt: tokens.AccessExpiresAt}
	require.NoError(t, h.Logout(ctx, p, client))

	assert.NotNil(t, h.sessions.byID[sid].RevokedAt)
	assert.Equal(t, 10*time.Minute+core.ClockLeeway, h.revocations.tokens["jti-1"])
	assert.Equal(t, 15*time.Minute+core.ClockLeeway, h.revocations.sessions[sid])
	assert.Equal(t, audit.EventLogout, h.audit.entries[len(h.audit.entries)-1].Event)

	_, err := h.Refresh(ctx, tokens.RefreshToken, client)
	assert.ErrorIs(t, err, ErrUnauthorized, "the refresh token died with the session")

	require.NoError(t, h.Logout(ctx, core.Principal{SessionID: "gone", TokenID: "jti-2"}, client), "logging out twice is fine")

	h.revocations.err = errDown
	assert.ErrorIs(t, h.Logout(ctx, p, client), ErrUnavailable)
	h.revocations.err = nil
	h.sessions.err = errDown
	assert.ErrorIs(t, h.Logout(ctx, p, client), errDown)
}

func TestLogoutDenyTokenFailure(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	h.login(t)
	rev := &failingDeny{memRevocations: h.revocations}
	h.Revocations = rev
	err := h.Logout(ctx, core.Principal{SessionID: "s", TokenID: "jti"}, client)
	assert.ErrorIs(t, err, ErrUnavailable)
}

type failingDeny struct{ *memRevocations }

func (f *failingDeny) DenyToken(context.Context, string, time.Duration) error { return errDown }

func TestTruncateKeepsRunesWhole(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 10))
	assert.Equal(t, "a", truncate("aé", 2))
}

func TestRateLimitedErrorMessage(t *testing.T) {
	assert.Equal(t, "rate limited, retry after 1m0s", (&RateLimitedError{RetryAfter: time.Minute}).Error())
}
