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
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/NickeAlves/monthra-server/internal/audit"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
	"github.com/NickeAlves/monthra-server/internal/testutil"
	"github.com/NickeAlves/monthra-server/migrations"
)

func testDB(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(testutil.StartMongo(t)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	db := client.Database("monthra_test")
	require.NoError(t, migrations.Apply(ctx, db, migrations.Options{AuditLogTTL: time.Hour}))
	return db
}

var now = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func TestUsers(t *testing.T) {
	ctx := context.Background()
	users := repo.NewUsers(testDB(t))

	u, err := users.Create(ctx, domain.NewUser("Ana", "ana@example.com", "hash-1", i18n.EsES, now))
	require.NoError(t, err)
	require.Len(t, u.ID, 24)

	_, err = users.Create(ctx, domain.NewUser("Other", "ana@example.com", "hash-2", i18n.PtBR, now))
	assert.ErrorIs(t, err, domain.ErrEmailTaken)

	byEmail, err := users.ByEmail(ctx, "ana@example.com")
	require.NoError(t, err)
	assert.Equal(t, u, byEmail)
	assert.Equal(t, domain.Preferences{Locale: i18n.EsES, Theme: domain.ThemeSystem, Currency: "BRL"}, byEmail.Preferences)
	assert.False(t, byEmail.EmailVerified())

	_, err = users.ByEmail(ctx, "nobody@example.com")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = users.ByID(ctx, "not-an-object-id")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = users.ByID(ctx, bson.NewObjectID().Hex())
	assert.ErrorIs(t, err, domain.ErrNotFound)

	require.NoError(t, users.MarkEmailVerified(ctx, u.ID, now.Add(time.Minute)))
	require.NoError(t, users.MarkEmailVerified(ctx, u.ID, now.Add(time.Hour)), "verifying again is fine")
	got, err := users.ByID(ctx, u.ID)
	require.NoError(t, err)
	require.NotNil(t, got.EmailVerifiedAt)
	assert.Equal(t, now.Add(time.Minute), got.EmailVerifiedAt.UTC(), "keeps the first date")
	assert.ErrorIs(t, users.MarkEmailVerified(ctx, bson.NewObjectID().Hex(), now), domain.ErrNotFound)

	require.NoError(t, users.UpdatePasswordHash(ctx, u.ID, "hash-3", now.Add(2*time.Hour)))
	got, err = users.ByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "hash-3", got.PasswordHash)
	assert.Equal(t, now.Add(2*time.Hour), got.UpdatedAt.UTC())
	assert.ErrorIs(t, users.UpdatePasswordHash(ctx, bson.NewObjectID().Hex(), "h", now), domain.ErrNotFound)
	assert.ErrorIs(t, users.UpdatePasswordHash(ctx, "bad", "h", now), domain.ErrNotFound)
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	sessions := repo.NewSessions(testDB(t))
	userID := bson.NewObjectID().Hex()

	s, err := sessions.Create(ctx, domain.Session{UserID: userID, UserAgent: "ua", IP: "198.51.100.7", CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)})
	require.NoError(t, err)
	got, err := sessions.ByID(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, userID, got.UserID)
	assert.True(t, got.Active(now))

	require.NoError(t, sessions.Touch(ctx, s.ID, now.Add(time.Minute), now.Add(2*time.Hour)))
	got, err = sessions.ByID(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, now.Add(time.Minute), got.LastUsedAt.UTC())
	assert.Equal(t, now.Add(2*time.Hour), got.ExpiresAt.UTC())

	require.NoError(t, sessions.Revoke(ctx, s.ID, now.Add(3*time.Minute)))
	require.NoError(t, sessions.Revoke(ctx, s.ID, now.Add(time.Hour)))
	require.NoError(t, sessions.Touch(ctx, s.ID, now.Add(time.Hour), now.Add(9*time.Hour)), "touching a revoked session changes nothing")
	got, err = sessions.ByID(ctx, s.ID)
	require.NoError(t, err)
	require.NotNil(t, got.RevokedAt)
	assert.Equal(t, now.Add(3*time.Minute), got.RevokedAt.UTC())
	assert.Equal(t, now.Add(2*time.Hour), got.ExpiresAt.UTC())
	assert.False(t, got.Active(now))

	_, err = sessions.Create(ctx, domain.Session{UserID: "bad"})
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = sessions.ByID(ctx, bson.NewObjectID().Hex())
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.ErrorIs(t, sessions.Touch(ctx, "bad", now, now), domain.ErrNotFound)
	assert.ErrorIs(t, sessions.Revoke(ctx, "bad", now), domain.ErrNotFound)
}

func TestRefreshTokensConsumeOnce(t *testing.T) {
	ctx := context.Background()
	tokens := repo.NewRefreshTokens(testDB(t))
	sid, uid := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	require.NoError(t, tokens.Create(ctx, domain.RefreshToken{TokenHash: "h1", SessionID: sid, UserID: uid, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}))

	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := tokens.Consume(ctx, "h1", now.Add(time.Minute)); err == nil {
				wins.Add(1)
			} else {
				assert.ErrorIs(t, err, domain.ErrNotFound)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load(), "exactly one concurrent refresh wins")

	used, err := tokens.ByHash(ctx, "h1")
	require.NoError(t, err)
	require.NotNil(t, used.UsedAt)
	assert.Equal(t, now.Add(time.Minute), used.UsedAt.UTC())
	assert.Equal(t, sid, used.SessionID)
	assert.Equal(t, uid, used.UserID)

	require.NoError(t, tokens.Create(ctx, domain.RefreshToken{TokenHash: "h2", SessionID: sid, UserID: uid, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}))
	_, err = tokens.Consume(ctx, "h2", now.Add(2*time.Hour))
	assert.ErrorIs(t, err, domain.ErrNotFound, "expired")
	_, err = tokens.ByHash(ctx, "missing")
	assert.ErrorIs(t, err, domain.ErrNotFound)

	assert.Error(t, tokens.Create(ctx, domain.RefreshToken{TokenHash: "h1", SessionID: sid, UserID: uid}), "hashes are unique")
	assert.ErrorIs(t, tokens.Create(ctx, domain.RefreshToken{TokenHash: "h3", SessionID: "bad", UserID: uid}), domain.ErrNotFound)
	assert.ErrorIs(t, tokens.Create(ctx, domain.RefreshToken{TokenHash: "h3", SessionID: sid, UserID: "bad"}), domain.ErrNotFound)
}

func TestAuthTokens(t *testing.T) {
	ctx := context.Background()
	tokens := repo.NewAuthTokens(testDB(t))
	uid := bson.NewObjectID().Hex()
	for _, h := range []string{"a1", "a2"} {
		require.NoError(t, tokens.Create(ctx, domain.AuthToken{TokenHash: h, UserID: uid, Purpose: domain.PurposeVerifyEmail, CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute)}))
	}

	_, err := tokens.Consume(ctx, "a1", "reset_password", now)
	assert.ErrorIs(t, err, domain.ErrNotFound, "the purpose must match")

	got, err := tokens.Consume(ctx, "a1", domain.PurposeVerifyEmail, now)
	require.NoError(t, err)
	assert.Equal(t, uid, got.UserID)
	_, err = tokens.Consume(ctx, "a1", domain.PurposeVerifyEmail, now)
	assert.ErrorIs(t, err, domain.ErrNotFound, "single use")

	require.NoError(t, tokens.InvalidateFor(ctx, uid, domain.PurposeVerifyEmail, now))
	_, err = tokens.Consume(ctx, "a2", domain.PurposeVerifyEmail, now)
	assert.ErrorIs(t, err, domain.ErrNotFound, "invalidated")

	assert.ErrorIs(t, tokens.Create(ctx, domain.AuthToken{TokenHash: "x", UserID: "bad"}), domain.ErrNotFound)
	assert.ErrorIs(t, tokens.InvalidateFor(ctx, "bad", domain.PurposeVerifyEmail, now), domain.ErrNotFound)
}

func TestAuditLogs(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	logs := repo.NewAuditLogs(db)
	uid, sid := bson.NewObjectID(), bson.NewObjectID()
	require.NoError(t, logs.Insert(ctx, audit.Entry{Event: audit.EventLoginSuccess, UserID: uid.Hex(), SessionID: sid.Hex(), IP: "198.51.100.7", UserAgent: "ua", CreatedAt: now}))
	require.NoError(t, logs.Insert(ctx, audit.Entry{Event: audit.EventLoginFailure, Reason: "unknown_user", CreatedAt: now}))

	var docs []bson.M
	cur, err := db.Collection(repo.CollAuditLogs).Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "event", Value: 1}}))
	require.NoError(t, err)
	require.NoError(t, cur.All(ctx, &docs))
	require.Len(t, docs, 2)
	assert.Equal(t, "login_failure", docs[0]["event"])
	assert.NotContains(t, docs[0], "user_id", "unknown actors are left out")
	assert.Equal(t, "unknown_user", docs[0]["reason"])
	assert.Equal(t, uid, docs[1]["user_id"])
	assert.Equal(t, sid, docs[1]["session_id"])
	assert.Equal(t, "198.51.100.7", docs[1]["ip"])
}
