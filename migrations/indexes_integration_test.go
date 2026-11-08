//go:build integration

package migrations

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
	"github.com/NickeAlves/monthra-server/internal/testutil"
)

func indexSpecs(t *testing.T, db *mongo.Database, coll string) map[string]mongo.IndexSpecification {
	t.Helper()
	specs, err := db.Collection(coll).Indexes().ListSpecifications(context.Background())
	require.NoError(t, err)
	out := make(map[string]mongo.IndexSpecification, len(specs))
	for _, s := range specs {
		out[s.Name] = s
	}
	return out
}

// indexName resolves the name from the v2 options builder.
func indexName(t *testing.T, m mongo.IndexModel) string {
	t.Helper()
	var resolved options.IndexOptions
	for _, set := range m.Options.List() {
		require.NoError(t, set(&resolved))
	}
	require.NotNil(t, resolved.Name)
	return *resolved.Name
}

func TestApplyCreatesIndexesIdempotently(t *testing.T) {
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(testutil.StartMongo(t)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	db := client.Database("monthra_test")

	opts := Options{AuditLogTTL: 24 * time.Hour}
	require.NoError(t, Apply(ctx, db, opts))
	require.NoError(t, Apply(ctx, db, opts), "second run is a no-op")

	for coll, models := range Indexes() {
		specs := indexSpecs(t, db, coll)
		for _, m := range models {
			name := indexName(t, m)
			assert.Containsf(t, specs, name, "index %s on %s", name, coll)
		}
	}

	users := indexSpecs(t, db, repo.CollUsers)
	require.NotNil(t, users["users_email_unique"].Unique)
	assert.True(t, *users["users_email_unique"].Unique)

	budgets := indexSpecs(t, db, repo.CollBudgets)
	require.NotNil(t, budgets["budgets_user_month_category_unique"].Unique)
	assert.True(t, *budgets["budgets_user_month_category_unique"].Unique)

	ttl := indexSpecs(t, db, repo.CollAuditLogs)[AuditLogTTLIndex]
	require.NotNil(t, ttl.ExpireAfterSeconds)
	assert.Equal(t, int32(86400), *ttl.ExpireAfterSeconds)

	for coll, name := range map[string]string{
		repo.CollSessions:      "sessions_ttl",
		repo.CollRefreshTokens: "refresh_tokens_ttl",
		repo.CollAuthTokens:    "auth_tokens_ttl",
	} {
		spec := indexSpecs(t, db, coll)[name]
		require.NotNil(t, spec.ExpireAfterSeconds, name)
		assert.Equal(t, int32(0), *spec.ExpireAfterSeconds, "%s expires each document at its expires_at", name)
	}
	for coll, name := range map[string]string{
		repo.CollRefreshTokens: "refresh_tokens_hash_unique",
		repo.CollAuthTokens:    "auth_tokens_hash_unique",
	} {
		spec := indexSpecs(t, db, coll)[name]
		require.NotNil(t, spec.Unique, name)
		assert.True(t, *spec.Unique, name)
	}
}

func TestApplyUpdatesChangedTTL(t *testing.T) {
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(testutil.StartMongo(t)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	db := client.Database("monthra_test")

	require.NoError(t, Apply(ctx, db, Options{AuditLogTTL: time.Hour}))
	require.NoError(t, Apply(ctx, db, Options{AuditLogTTL: 2 * time.Hour}))

	ttl := indexSpecs(t, db, repo.CollAuditLogs)[AuditLogTTLIndex]
	require.NotNil(t, ttl.ExpireAfterSeconds)
	assert.Equal(t, int32(7200), *ttl.ExpireAfterSeconds)
}

func TestApplyRejectsInvalidTTL(t *testing.T) {
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(testutil.StartMongo(t)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })

	err = Apply(ctx, client.Database("monthra_test"), Options{AuditLogTTL: 0})
	assert.ErrorContains(t, err, "TTL must be between")
}
