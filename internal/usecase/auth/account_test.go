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

var anaKey = "e:" + core.HashIdentifier("email", "ana@example.com")

// signIn logs in with the password and returns the principal its access
// token stands for (the fake token is "at.{user}.{session}.{jti}").
func (h *harness) signIn(t *testing.T) core.Principal {
	t.Helper()
	tokens := h.login(t)
	parts := strings.Split(tokens.AccessToken, ".")
	require.Len(t, parts, 4)
	return core.Principal{UserID: parts[1], SessionID: parts[2], TokenID: parts[3], ExpiresAt: tokens.AccessExpiresAt}
}

func TestChangePasswordKeepsOnlyTheCurrentSession(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	other := h.signIn(t)
	h.clock.advance(time.Minute)
	p := h.signIn(t)
	require.NoError(t, h.ForgotPassword(ctx, "ana@example.com", client))
	require.Equal(t, 1, h.authTokens.pending())

	in := ChangePasswordInput{CurrentPassword: password, NewPassword: newPassword}
	require.NoError(t, h.ChangePassword(ctx, p, in, client))

	assert.Equal(t, "v1:"+newPassword, h.users.only().PasswordHash)
	assert.True(t, h.revocations.sessionRevoked(other.SessionID), "other sessions lose their access tokens")
	assert.False(t, h.revocations.sessionRevoked(p.SessionID), "the current one stays")
	assert.Equal(t, 1, h.sessions.active(p.UserID, h.clock.now()))
	assert.Zero(t, h.authTokens.pending(), "pending reset links stop working")
	assert.Contains(t, h.audit.events(), audit.EventPasswordChanged)
	last := h.mail.sent[len(h.mail.sent)-1]
	assert.Equal(t, email.TemplatePasswordChanged, last.Template)
	assert.Equal(t, i18n.EsES, last.Locale)

	_, err := h.Login(ctx, LoginInput{Email: "ana@example.com", Password: password}, client)
	require.ErrorIs(t, err, ErrInvalidCredentials, "the old password is gone")
	_, err = h.Login(ctx, LoginInput{Email: "ana@example.com", Password: newPassword}, client)
	require.NoError(t, err)
}

func TestChangePasswordRejections(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	h.signIn(t) // another device
	p := h.signIn(t)
	before := h.users.only().PasswordHash

	err := h.ChangePassword(ctx, p, ChangePasswordInput{CurrentPassword: password, NewPassword: "short"}, client)
	assert.Equal(t, []domain.FieldError{{Field: "newPassword", Code: domain.CodeTooShort}}, fieldsOf(t, err))
	assert.Zero(t, h.lockout.fails[anaKey], "a policy error is not a guess")

	err = h.ChangePassword(ctx, p, ChangePasswordInput{CurrentPassword: "wrong password!", NewPassword: newPassword}, client)
	require.ErrorIs(t, err, ErrPasswordIncorrect)
	assert.Equal(t, 1, h.lockout.fails[anaKey], "a wrong current password counts toward the lockout")
	assert.Contains(t, h.audit.events(), audit.EventReauthFailure)

	h.breach.breached = true
	err = h.ChangePassword(ctx, p, ChangePasswordInput{CurrentPassword: password, NewPassword: newPassword}, client)
	assert.Equal(t, []domain.FieldError{{Field: "newPassword", Code: domain.CodeBreached}}, fieldsOf(t, err))
	assert.Equal(t, before, h.users.only().PasswordHash, "nothing changed")
	h.breach.breached = false

	h.revocations.err = errDown
	err = h.ChangePassword(ctx, p, ChangePasswordInput{CurrentPassword: password, NewPassword: newPassword}, client)
	require.ErrorIs(t, err, ErrUnavailable)
	assert.Equal(t, before, h.users.only().PasswordHash, "Redis down changes nothing, so the retry uses the same current password")
	h.revocations.err = nil

	h.hasher.err = errDown
	err = h.ChangePassword(ctx, p, ChangePasswordInput{CurrentPassword: password, NewPassword: newPassword}, client)
	require.Error(t, err)
	h.hasher.err = nil

	gone := core.Principal{UserID: idFor("u", 99), SessionID: p.SessionID}
	err = h.ChangePassword(ctx, gone, ChangePasswordInput{CurrentPassword: password, NewPassword: newPassword}, client)
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestRevokeAllSessionsDeniesBeforeRevoking(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	other := h.signIn(t)
	p := h.signIn(t)

	h.revocations.err = errDown
	require.ErrorIs(t, h.RevokeOtherSessions(ctx, p, client), ErrUnavailable)
	assert.Equal(t, 2, h.sessions.active(p.UserID, h.clock.now()), "Redis failing leaves MongoDB untouched")

	h.revocations.err = nil
	require.NoError(t, h.RevokeOtherSessions(ctx, p, client), "so a retry denies the same sessions")
	assert.True(t, h.revocations.sessionRevoked(other.SessionID))
	assert.False(t, h.revocations.sessionRevoked(p.SessionID))
	assert.Equal(t, 1, h.sessions.active(p.UserID, h.clock.now()))
	assert.Contains(t, h.audit.events(), audit.EventSessionsRevoked)

	h.sessions.err = errDown
	require.Error(t, h.RevokeOtherSessions(ctx, p, client))
	assert.Equal(t, []string{"a", "c"}, missing([]string{"a", "b", "c"}, []string{"b"}))
}

func TestListSessions(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	first := h.signIn(t)
	h.clock.advance(time.Minute)
	p := h.signIn(t)

	list, err := h.ListSessions(ctx, p)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, p.SessionID, list[0].ID, "most recently used first")
	assert.True(t, list[0].Current)
	assert.Equal(t, first.SessionID, list[1].ID)
	assert.False(t, list[1].Current)
	assert.Equal(t, "198.51.100.7", list[0].IP)
	assert.Equal(t, "Mozilla/5.0 test", list[0].UserAgent)

	h.sessions.err = errDown
	_, err = h.ListSessions(ctx, p)
	require.Error(t, err)
}

func TestRevokeSession(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	other := h.signIn(t)
	p := h.signIn(t)
	stranger := h.sessions.byID[other.SessionID]
	stranger.UserID = idFor("u", 77)
	h.sessions.byID["s-stranger"] = stranger

	for name, id := range map[string]string{"another user's": "s-stranger", "unknown": "s-unknown"} {
		_, err := h.RevokeSession(ctx, p, id, client)
		require.ErrorIs(t, err, ErrSessionNotFound, name)
	}
	assert.False(t, h.revocations.sessionRevoked("s-stranger"))

	h.revocations.err = errDown
	_, err := h.RevokeSession(ctx, p, other.SessionID, client)
	require.ErrorIs(t, err, ErrUnavailable)
	h.revocations.err = nil

	current, err := h.RevokeSession(ctx, p, other.SessionID, client)
	require.NoError(t, err, "retrying an owned session works")
	assert.False(t, current)
	assert.True(t, h.revocations.sessionRevoked(other.SessionID))
	assert.Contains(t, h.audit.events(), audit.EventSessionRevoked)

	current, err = h.RevokeSession(ctx, p, p.SessionID, client)
	require.NoError(t, err)
	assert.True(t, current, "the caller may sign out its own session")

	h.sessions.err = errDown
	_, err = h.RevokeSession(ctx, p, other.SessionID, client)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrSessionNotFound)
}

func TestDeleteAccountErasesEverything(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)
	other := h.signIn(t)
	p := h.signIn(t)
	h.lockout.fails = map[string]int{anaKey: 2}

	require.NoError(t, h.DeleteAccount(ctx, p, DeleteAccountInput{Password: password}, client))

	assert.Zero(t, h.users.count(), "the account is gone")
	assert.Equal(t, []string{"transactions:" + u.ID, "sessions:" + u.ID}, h.erased.ran, "erasers run in order")
	for _, sid := range []string{other.SessionID, p.SessionID} {
		assert.True(t, h.revocations.sessionRevoked(sid), "every session is denied")
	}
	assert.Contains(t, h.revocations.tokens, p.TokenID)
	assert.Zero(t, h.lockout.fails[anaKey], "a new account on the address starts clean")

	events := h.audit.events()
	assert.Equal(t, audit.EventAccountDeleted, events[len(events)-1])
	last := h.mail.sent[len(h.mail.sent)-1]
	assert.Equal(t, email.TemplateAccountDeleted, last.Template)
	assert.Equal(t, "ana@example.com", last.To)
	assert.Equal(t, i18n.EsES, last.Locale)
	assert.Empty(t, last.Link)

	err := h.DeleteAccount(ctx, p, DeleteAccountInput{Password: password}, client)
	require.ErrorIs(t, err, ErrUnauthorized, "a deleted account has no session")
}

func TestDeleteAccountAsksForThePassword(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	p := h.signIn(t)

	err := h.DeleteAccount(ctx, p, DeleteAccountInput{Password: "wrong password!"}, client)
	require.ErrorIs(t, err, ErrPasswordIncorrect)
	assert.Equal(t, 1, h.users.count())
	assert.Empty(t, h.erased.ran)
	assert.Equal(t, 1, h.lockout.fails[anaKey])
}

func TestDeleteAccountWithMFAAsksForACode(t *testing.T) {
	h := newHarness()
	_, codes := h.mfaUser(t)
	p := h.principal()

	err := h.DeleteAccount(ctx, p, DeleteAccountInput{Password: password}, client)
	require.ErrorIs(t, err, ErrMFACodeInvalid, "a code is required")
	assert.Zero(t, h.lockout.fails[anaKey], "a missing code is not a guess")

	err = h.DeleteAccount(ctx, p, DeleteAccountInput{Password: password, Code: "000000"}, client)
	require.ErrorIs(t, err, ErrMFACodeInvalid)
	assert.Equal(t, 1, h.lockout.fails[anaKey], "a wrong code counts toward the lockout")
	assert.Contains(t, h.audit.events(), audit.EventMFAFailure)
	assert.Equal(t, 1, h.users.count())

	require.NoError(t, h.DeleteAccount(ctx, p, DeleteAccountInput{Password: password, Code: codes[0]}, client))
	assert.Zero(t, h.users.count())
	entry := h.audit.entries[len(h.audit.entries)-1]
	assert.Equal(t, audit.EventAccountDeleted, entry.Event)
	assert.Equal(t, factorRecoveryCode, entry.Reason, "records how the second factor was checked")
}

func TestDeleteAccountCanBeRetried(t *testing.T) {
	h := newHarness()
	u := h.verifiedUser(t)
	p := h.signIn(t)

	h.revocations.err = errDown
	require.ErrorIs(t, h.DeleteAccount(ctx, p, DeleteAccountInput{Password: password}, client), ErrUnavailable)
	assert.Empty(t, h.erased.ran, "nothing is erased while tokens cannot be denied")
	h.revocations.err = nil

	h.erased.fail = "sessions"
	err := h.DeleteAccount(ctx, p, DeleteAccountInput{Password: password}, client)
	require.Error(t, err)
	assert.Equal(t, 1, h.users.count(), "a failed eraser keeps the account, so its owner can retry")

	// The sessions are revoked by now, so the owner signs in again to retry.
	h.erased.fail = ""
	p = h.signIn(t)
	require.NoError(t, h.DeleteAccount(ctx, p, DeleteAccountInput{Password: password}, client))
	assert.Zero(t, h.users.count())
	assert.Equal(t, []string{"transactions:" + u.ID, "transactions:" + u.ID, "sessions:" + u.ID}, h.erased.ran, "erasers run again")

	h2 := newHarness()
	h2.verifiedUser(t)
	p2 := h2.signIn(t)
	h2.users.err = errDown
	require.Error(t, h2.DeleteAccount(ctx, p2, DeleteAccountInput{Password: password}, client))
}

func TestDeleteAccountFailsWhenTheAccountCannotBeDeleted(t *testing.T) {
	h := newHarness()
	h.verifiedUser(t)
	p := h.signIn(t)
	h.users.deleteErr = errDown
	err := h.DeleteAccount(ctx, p, DeleteAccountInput{Password: password}, client)
	require.Error(t, err)
	assert.NotContains(t, h.audit.events(), audit.EventAccountDeleted)
}
