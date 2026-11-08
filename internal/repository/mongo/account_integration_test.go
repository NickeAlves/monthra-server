//go:build integration

package mongo_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

func TestUsersProfileAndDelete(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	users := repo.NewUsers(db)
	u, err := users.Create(ctx, domain.NewUser("Ana", "ana@example.com", "$argon2id$hash", i18n.EsES, now))
	require.NoError(t, err)

	l, err := users.Locale(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, i18n.EsES, l)

	require.NoError(t, users.UpdateName(ctx, u.ID, "Ana Maria", now.Add(time.Minute)))
	prefs := domain.Preferences{Locale: i18n.EnUS, Theme: domain.ThemeDark, Currency: domain.CurrencyEUR}
	require.NoError(t, users.UpdatePreferences(ctx, u.ID, prefs, now.Add(2*time.Minute)))

	got, err := users.ByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "Ana Maria", got.Name)
	assert.Equal(t, prefs, got.Preferences)
	assert.Equal(t, now.Add(2*time.Minute), got.UpdatedAt.UTC())
	assert.Equal(t, "$argon2id$hash", got.PasswordHash, "other fields are untouched")
	l, err = users.Locale(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, i18n.EnUS, l)

	missing := bson.NewObjectID().Hex()
	assert.ErrorIs(t, users.UpdateName(ctx, missing, "x", now), domain.ErrNotFound)
	assert.ErrorIs(t, users.UpdatePreferences(ctx, "bad", prefs, now), domain.ErrNotFound)
	_, err = users.Locale(ctx, missing)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = users.Locale(ctx, "bad")
	assert.ErrorIs(t, err, domain.ErrNotFound)

	require.NoError(t, users.Delete(ctx, u.ID))
	_, err = users.ByID(ctx, u.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, users.Delete(ctx, u.ID), "deleting twice is fine")
	require.NoError(t, users.Delete(ctx, "bad"))

	_, err = users.Create(ctx, domain.NewUser("Ana", "ana@example.com", "h", i18n.PtBR, now))
	require.NoError(t, err, "the address is free again")
}

func TestSessionsListAndRevokeForUser(t *testing.T) {
	ctx := context.Background()
	sessions := repo.NewSessions(testDB(t))
	owner, other := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	create := func(userID string, lastUsed, expires time.Duration) domain.Session {
		t.Helper()
		s, err := sessions.Create(ctx, domain.Session{UserID: userID, UserAgent: "ua", IP: "198.51.100.7",
			CreatedAt: now, LastUsedAt: now.Add(lastUsed), ExpiresAt: now.Add(expires)})
		require.NoError(t, err)
		return s
	}
	older := create(owner, time.Minute, time.Hour)
	newer := create(owner, 2*time.Minute, time.Hour)
	expired := create(owner, 3*time.Minute, -time.Minute)
	revoked := create(owner, 4*time.Minute, time.Hour)
	require.NoError(t, sessions.Revoke(ctx, revoked.ID, now))
	foreign := create(other, 5*time.Minute, time.Hour)

	list, err := sessions.ListActive(ctx, owner, now, 100)
	require.NoError(t, err)
	require.Len(t, list, 2, "expired, revoked and foreign sessions are left out")
	assert.Equal(t, []string{newer.ID, older.ID}, []string{list[0].ID, list[1].ID}, "most recently used first")
	assert.Equal(t, "198.51.100.7", list[0].IP)

	list, err = sessions.ListActive(ctx, owner, now, 1)
	require.NoError(t, err)
	assert.Len(t, list, 1, "limit")
	_, err = sessions.ListActive(ctx, "bad", now, 10)
	assert.ErrorIs(t, err, domain.ErrNotFound)

	for name, id := range map[string]string{
		"another user's": foreign.ID,
		"expired":        expired.ID,
		"unknown":        bson.NewObjectID().Hex(),
		"malformed":      "bad",
	} {
		ok, err := sessions.RevokeForUser(ctx, owner, id, now)
		require.NoError(t, err, name)
		assert.False(t, ok, name)
	}
	got, err := sessions.ByID(ctx, foreign.ID)
	require.NoError(t, err)
	assert.True(t, got.Active(now), "the other user's session is untouched")

	ok, err := sessions.RevokeForUser(ctx, owner, older.ID, now.Add(time.Second))
	require.NoError(t, err)
	assert.True(t, ok)
	got, err = sessions.ByID(ctx, older.ID)
	require.NoError(t, err)
	assert.False(t, got.Active(now.Add(time.Second)))
	ok, err = sessions.RevokeForUser(ctx, owner, older.ID, now.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, ok, "revoking again is safe to retry")
	got, err = sessions.ByID(ctx, older.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RevokedAt)
	assert.Equal(t, now.Add(time.Second), got.RevokedAt.UTC(), "keeps the first date")
	ok, err = sessions.RevokeForUser(ctx, owner, revoked.ID, now)
	require.NoError(t, err)
	assert.True(t, ok, "an owned, revoked but unexpired session")

	ids, err := sessions.ActiveIDs(ctx, owner, "", now)
	require.NoError(t, err)
	assert.Equal(t, []string{newer.ID}, ids)
	ids, err = sessions.ActiveIDs(ctx, owner, newer.ID, now)
	require.NoError(t, err)
	assert.Empty(t, ids, "except leaves the current session out")
	_, err = sessions.ActiveIDs(ctx, owner, "bad", now)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = sessions.ActiveIDs(ctx, "bad", "", now)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestUserEraser(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	sessions := repo.NewSessions(db)
	owner, other := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	for _, uid := range []string{owner, owner, other} {
		_, err := sessions.Create(ctx, domain.Session{UserID: uid, CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)})
		require.NoError(t, err)
	}

	eraser := repo.NewUserEraser(db, repo.CollSessions)
	require.NoError(t, eraser.EraseUserData(ctx, owner))
	require.NoError(t, eraser.EraseUserData(ctx, owner), "erasing twice is fine")
	require.NoError(t, eraser.EraseUserData(ctx, "bad"))

	n, err := db.Collection(repo.CollSessions).CountDocuments(ctx, bson.D{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "only the other user's session is left")
	list, err := sessions.ListActive(ctx, other, now, 10)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}
