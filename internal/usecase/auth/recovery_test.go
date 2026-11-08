package auth

import (
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

const newPassword = "a brand new passphrase"

// resetToken asks for a reset of ana@example.com and returns the token.
func (h *harness) resetToken(t *testing.T) string {
	t.Helper()
	require.NoError(t, h.ForgotPassword(ctx, "Ana@Example.com", client))
	msg := h.mail.sent[len(h.mail.sent)-1]
	require.Equal(t, email.TemplateResetPassword, msg.Template)
	return tokenFromLink(t, msg.Link)
}

func TestForgotPasswordSendsASingleUseLink(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)
	sent := len(h.mail.sent)

	require.NoError(t, h.ForgotPassword(ctx, " ANA@example.com ", client))
	require.Len(t, h.mail.sent, sent+1)
	msg := h.mail.sent[sent]
	assert.Equal(t, email.TemplateResetPassword, msg.Template)
	assert.Equal(t, "ana@example.com", msg.To)
	assert.Equal(t, i18n.EsES, msg.Locale, "the account locale")
	assert.True(t, strings.HasPrefix(msg.Link, "https://app.monthra.example/reset-password#token="), msg.Link)
	assert.Equal(t, 15*time.Minute, msg.LinkTTL)
	assert.Equal(t, 1, countEvents(h.audit, audit.EventPasswordResetRequest))
	assert.Equal(t, u.ID, h.audit.entries[len(h.audit.entries)-1].UserID)

	first := tokenFromLink(t, msg.Link)
	second := h.resetToken(t)
	assert.ErrorIs(t, h.ResetPassword(ctx, ResetPasswordInput{Token: first, Password: newPassword}, client), ErrTokenInvalid, "only the newest link works")
	require.NoError(t, h.ResetPassword(ctx, ResetPasswordInput{Token: second, Password: newPassword}, client))
}

func TestForgotPasswordOfAnUnknownAddressLooksTheSame(t *testing.T) {
	h := newHarness()
	require.NoError(t, h.ForgotPassword(ctx, "nobody@example.com", client))
	assert.Empty(t, h.mail.sent)
	assert.Zero(t, h.authTokens.pending())
	assert.Empty(t, h.audit.entries)
}

func TestForgotPasswordValidatesAndLimits(t *testing.T) {
	h := newHarness()
	assert.Equal(t, []domain.FieldError{{Field: "email", Code: domain.CodeInvalid}}, fieldsOf(t, h.ForgotPassword(ctx, "not an email", client)))

	h.mailLimiter.limit = 1
	require.NoError(t, h.ForgotPassword(ctx, "nobody@example.com", client))
	var limited *RateLimitedError
	require.ErrorAs(t, h.ForgotPassword(ctx, "nobody@example.com", client), &limited, "the quota applies whether or not the address exists")
	assert.Equal(t, 42*time.Second, limited.RetryAfter)
	assert.Equal(t, 1, h.metrics.rateLimited[PolicyAuthMail])
	assert.Equal(t, core.HashIdentifier("email", "nobody@example.com"), h.mailLimiter.subjects[0], "addresses are hashed")

	h = newHarness()
	h.limiter.limit = 1
	require.NoError(t, h.ForgotPassword(ctx, "other@example.com", client))
	require.ErrorAs(t, h.ForgotPassword(ctx, "other@example.com", client), &limited, "and so does the credential budget")
	assert.Equal(t, 1, h.metrics.rateLimited[PolicyAuthByEmail])
}

func TestMailQuotaCoversSignUpAndResend(t *testing.T) {
	h := newHarness()
	h.mailLimiter.limit = 2
	require.NoError(t, h.Register(ctx, registerInput(), client))
	require.NoError(t, h.ResendVerification(ctx, "ana@example.com", client))
	var limited *RateLimitedError
	assert.ErrorAs(t, h.ResendVerification(ctx, "ana@example.com", client), &limited)
	assert.ErrorAs(t, h.Register(ctx, registerInput(), client), &limited)
	assert.Len(t, h.mail.sent, 2)
}

func TestForgotPasswordReportsStoreFailures(t *testing.T) {
	h := newHarness()
	h.mailLimiter.err = errDown
	assert.ErrorIs(t, h.ForgotPassword(ctx, "ana@example.com", client), ErrUnavailable)

	h = newHarness()
	h.verifiedUser(t)
	h.authTokens.err = errDown
	assert.ErrorIs(t, h.ForgotPassword(ctx, "ana@example.com", client), errDown)

	h = newHarness()
	h.users.err = errDown
	assert.ErrorIs(t, h.ForgotPassword(ctx, "ana@example.com", client), errDown)
}

func TestResetPasswordChangesThePasswordAndSignsEverywhereOut(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)
	h.login(t)
	h.login(t)
	require.Equal(t, 2, h.sessions.active(u.ID, h.clock.now()))
	for range 3 {
		_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: "wrong password!"}, client)
		require.ErrorIs(t, err, ErrInvalidCredentials)
	}
	token := h.resetToken(t)

	require.NoError(t, h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: newPassword}, client))
	got := h.users.only()
	assert.Equal(t, "v1:"+newPassword, got.PasswordHash)
	assert.Zero(t, h.sessions.active(u.ID, h.clock.now()), "every session is revoked")
	for id := range h.sessions.byID {
		assert.True(t, h.revocations.sessionRevoked(id), "and denied in Redis: %s", id)
	}
	assert.Zero(t, h.lockout.fails["e:"+core.HashIdentifier("email", "ana@example.com")], "the owner proved control: failures are forgotten")
	assert.Equal(t, 1, countEvents(h.audit, audit.EventPasswordReset))
	assert.Equal(t, 1, h.metrics.resets)

	notice := h.mail.sent[len(h.mail.sent)-1]
	assert.Equal(t, email.TemplatePasswordChanged, notice.Template)
	assert.Equal(t, "ana@example.com", notice.To)
	assert.Equal(t, "https://app.monthra.example/forgot-password", notice.Link)

	assert.ErrorIs(t, h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: newPassword}, client), ErrTokenInvalid, "single use")
	_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	assert.ErrorIs(t, err, ErrInvalidCredentials, "the old password is gone")
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: newPassword}, client)
	assert.NoError(t, err)
}

func TestResetPasswordVerifiesTheAddress(t *testing.T) {
	h := newHarness()
	require.NoError(t, h.Register(ctx, registerInput(), client))
	require.False(t, h.users.only().EmailVerified())

	require.NoError(t, h.ResetPassword(ctx, ResetPasswordInput{Token: h.resetToken(t), Password: newPassword}, client))
	assert.True(t, h.users.only().EmailVerified(), "following the link proves control of the inbox")
	_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: newPassword}, client)
	assert.NoError(t, err)
}

func TestResetPasswordRejectsBeforeSpendingTheToken(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	token := h.resetToken(t)

	err := h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: "short"}, client)
	assert.Equal(t, []domain.FieldError{{Field: "password", Code: domain.CodeTooShort}}, fieldsOf(t, err))
	h.breach.breached = true
	err = h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: newPassword}, client)
	assert.Equal(t, []domain.FieldError{{Field: "password", Code: domain.CodeBreached}}, fieldsOf(t, err))
	h.breach.breached = false
	h.hasher.err = errDown
	assert.ErrorIs(t, h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: newPassword}, client), errDown)
	h.hasher.err = nil

	require.NoError(t, h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: newPassword}, client), "the link still works")
}

func TestResetPasswordRejectsBadTokens(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	for _, token := range []string{"", "short", strings.Repeat("A", core.OpaqueTokenLength), core.NewOpaqueToken()} {
		assert.ErrorIs(t, h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: newPassword}, client), ErrTokenInvalid, "%q", token)
	}

	token := h.resetToken(t)
	h.clock.advance(16 * time.Minute)
	assert.ErrorIs(t, h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: newPassword}, client), ErrTokenInvalid, "expired")
}

func TestResetPasswordDoesNotAcceptVerificationTokens(t *testing.T) {
	h := newHarness()
	require.NoError(t, h.Register(ctx, registerInput(), client))
	verify := tokenFromLink(t, h.mail.sent[0].Link)
	assert.ErrorIs(t, h.ResetPassword(ctx, ResetPasswordInput{Token: verify, Password: newPassword}, client), ErrTokenInvalid)
	reset := h.resetToken(t)
	assert.ErrorIs(t, h.VerifyEmail(ctx, reset, client), ErrTokenInvalid)
}

func TestResetPasswordOfADeletedAccount(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)
	token := h.resetToken(t)
	delete(h.users.byID, u.ID)
	assert.ErrorIs(t, h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: newPassword}, client), ErrTokenInvalid)
}

func TestResetPasswordReportsStoreFailures(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	h.login(t)
	token := h.resetToken(t)
	h.revocations.err = errDown
	assert.ErrorIs(t, h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: newPassword}, client), ErrUnavailable, "the denylist fails closed")

	h = newHarness()
	h.verifiedUser(t)
	token = h.resetToken(t)
	h.sessions.err = errDown
	assert.ErrorIs(t, h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: newPassword}, client), errDown)

	h = newHarness()
	h.verifiedUser(t)
	token = h.resetToken(t)
	h.authTokens.err = errDown
	assert.ErrorIs(t, h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: newPassword}, client), errDown)

	h = newHarness()
	h.verifiedUser(t)
	token = h.resetToken(t)
	h.users.err = errDown
	assert.ErrorIs(t, h.ResetPassword(ctx, ResetPasswordInput{Token: token, Password: newPassword}, client), errDown)
}
