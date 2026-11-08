//go:build integration

package mongo_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

func TestUsersResetPassword(t *testing.T) {
	ctx := context.Background()
	users := repo.NewUsers(testDB(t))

	unverified, err := users.Create(ctx, domain.NewUser("Ana", "ana@example.com", "hash-1", i18n.PtBR, now))
	require.NoError(t, err)
	require.NoError(t, users.ResetPassword(ctx, unverified.ID, "hash-2", now.Add(time.Minute)))
	got, err := users.ByID(ctx, unverified.ID)
	require.NoError(t, err)
	assert.Equal(t, "hash-2", got.PasswordHash)
	require.NotNil(t, got.EmailVerifiedAt, "the reset link proves control of the inbox")
	assert.Equal(t, now.Add(time.Minute), got.EmailVerifiedAt.UTC())
	assert.Equal(t, now.Add(time.Minute), got.UpdatedAt.UTC())

	const phc = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"
	require.NoError(t, users.ResetPassword(ctx, unverified.ID, phc, now.Add(time.Hour)))
	got, err = users.ByID(ctx, unverified.ID)
	require.NoError(t, err)
	assert.Equal(t, phc, got.PasswordHash, "a value starting with $ is stored as is, not read as a field path")
	assert.Equal(t, now.Add(time.Minute), got.EmailVerifiedAt.UTC(), "keeps the first verification date")

	assert.ErrorIs(t, users.ResetPassword(ctx, bson.NewObjectID().Hex(), "h", now), domain.ErrNotFound)
	assert.ErrorIs(t, users.ResetPassword(ctx, "bad", "h", now), domain.ErrNotFound)
}

func TestUsersMFALifecycle(t *testing.T) {
	ctx := context.Background()
	users := repo.NewUsers(testDB(t))
	u, err := users.Create(ctx, domain.NewUser("Ana", "ana@example.com", "hash", i18n.PtBR, now))
	require.NoError(t, err)
	assert.False(t, u.MFAEnabled())
	assert.Equal(t, domain.MFA{}, u.MFA)

	ok, err := users.SetPendingMFA(ctx, u.ID, "e1:first", now.Add(15*time.Minute), now)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = users.SetPendingMFA(ctx, u.ID, "e1:second", now.Add(20*time.Minute), now)
	require.NoError(t, err)
	assert.True(t, ok, "a new setup replaces the pending one")
	got, err := users.ByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "e1:second", got.MFA.PendingSecret)
	assert.True(t, got.MFA.PendingActive(now))
	assert.False(t, got.MFA.PendingActive(now.Add(time.Hour)))
	assert.False(t, got.MFAEnabled())

	ok, err = users.EnableMFA(ctx, u.ID, "e1:first", []string{"r1"}, 100, now)
	require.NoError(t, err)
	assert.False(t, ok, "a replaced setup cannot be enabled")
	ok, err = users.EnableMFA(ctx, u.ID, "e1:second", []string{"r1", "r2"}, 100, now.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, ok, "an expired setup cannot be enabled")

	enableAt := now.Add(time.Minute)
	ok, err = users.EnableMFA(ctx, u.ID, "e1:second", []string{"r1", "r2"}, 100, enableAt)
	require.NoError(t, err)
	require.True(t, ok)
	got, err = users.ByID(ctx, u.ID)
	require.NoError(t, err)
	require.True(t, got.MFAEnabled())
	assert.Equal(t, domain.MFA{Secret: "e1:second", EnabledAt: got.MFA.EnabledAt, RecoveryCodes: []string{"r1", "r2"}, LastStep: 100}, got.MFA)
	assert.Equal(t, enableAt, got.MFA.EnabledAt.UTC())

	ok, err = users.EnableMFA(ctx, u.ID, "e1:second", []string{"x"}, 101, enableAt)
	require.NoError(t, err)
	assert.False(t, ok, "enabling twice fails")
	ok, err = users.SetPendingMFA(ctx, u.ID, "e1:third", now.Add(time.Hour), now)
	require.NoError(t, err)
	assert.False(t, ok, "no setup while enabled")

	for step, want := range map[int64]bool{100: false, 99: false, 101: true} {
		ok, err = users.UseTOTPStep(ctx, u.ID, step)
		require.NoError(t, err)
		assert.Equal(t, want, ok, "step %d", step)
	}
	ok, err = users.UseTOTPStep(ctx, u.ID, 101)
	require.NoError(t, err)
	assert.False(t, ok, "a step works once")

	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if ok, err := users.UseRecoveryCode(ctx, u.ID, "r1"); err == nil && ok {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load(), "a recovery code works once, even concurrently")
	ok, err = users.UseRecoveryCode(ctx, u.ID, "unknown")
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = users.ReplaceMFASecret(ctx, u.ID, "e1:stale", "e2:new")
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = users.ReplaceMFASecret(ctx, u.ID, "e1:second", "e2:new")
	require.NoError(t, err)
	assert.True(t, ok)
	got, err = users.ByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "e2:new", got.MFA.Secret)
	assert.Equal(t, []string{"r2"}, got.MFA.RecoveryCodes)

	ok, err = users.DisableMFA(ctx, u.ID, now.Add(2*time.Hour))
	require.NoError(t, err)
	assert.True(t, ok)
	got, err = users.ByID(ctx, u.ID)
	require.NoError(t, err)
	assert.False(t, got.MFAEnabled())
	assert.Equal(t, domain.MFA{}, got.MFA, "nothing of the old setup is left")
	ok, err = users.DisableMFA(ctx, u.ID, now)
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = users.UseTOTPStep(ctx, u.ID, 500)
	require.NoError(t, err)
	assert.False(t, ok, "no steps while disabled")

	_, err = users.SetPendingMFA(ctx, "bad", "s", now, now)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestSessionsRevokeAllForUser(t *testing.T) {
	ctx := context.Background()
	sessions := repo.NewSessions(testDB(t))
	at := time.Now().UTC().Truncate(time.Millisecond) // sessions have a TTL index: stay in the future
	userID, otherUser := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	create := func(uid string, expires time.Time) domain.Session {
		s, err := sessions.Create(ctx, domain.Session{UserID: uid, CreatedAt: at, LastUsedAt: at, ExpiresAt: expires})
		require.NoError(t, err)
		return s
	}
	current := create(userID, at.Add(time.Hour))
	second := create(userID, at.Add(time.Hour))
	third := create(userID, at.Add(time.Hour))
	expired := create(userID, at.Add(time.Second))
	foreign := create(otherUser, at.Add(time.Hour))
	require.NoError(t, sessions.Revoke(ctx, third.ID, at))

	ids, err := sessions.RevokeAllForUser(ctx, userID, current.ID, at.Add(2*time.Second))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{second.ID}, ids, "skips the kept, revoked and expired sessions")

	for id, active := range map[string]bool{current.ID: true, second.ID: false, foreign.ID: true, expired.ID: true} {
		s, err := sessions.ByID(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, active, s.RevokedAt == nil, id)
	}

	ids, err = sessions.RevokeAllForUser(ctx, userID, "", at.Add(2*time.Second))
	require.NoError(t, err)
	assert.Equal(t, []string{current.ID}, ids)
	ids, err = sessions.RevokeAllForUser(ctx, userID, "", at)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{expired.ID}, ids, "the expired one was still active at that time")
	ids, err = sessions.RevokeAllForUser(ctx, userID, "", at)
	require.NoError(t, err)
	assert.Empty(t, ids)

	_, err = sessions.RevokeAllForUser(ctx, "bad", "", at)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = sessions.RevokeAllForUser(ctx, userID, "bad", at)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}
