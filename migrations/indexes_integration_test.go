//go:build integration

package migrations

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
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

	defaults := indexSpecs(t, db, repo.CollCategories)["categories_user_default_key_unique"]
	require.NotNil(t, defaults.Unique)
	assert.True(t, *defaults.Unique, "one default category per key and user")

	// Only the card legs carry an invoice, only installments a group, only
	// occurrences a recurrence, and only active recurrences a next day.
	for name, coll := range map[string]string{
		"transactions_user_account_invoice_date_id":    repo.CollTransactions,
		"transactions_user_to_account_invoice_date_id": repo.CollTransactions,
		"transactions_user_installment_date_id":        repo.CollTransactions,
		"transactions_user_recurrence_date_id":         repo.CollTransactions,
		"transactions_recurrence_occurrence_unique":    repo.CollTransactions,
		"recurrences_user_next":                        repo.CollRecurrences,
		"investment_movements_user_income_date":        repo.CollInvestmentMovements,
		"investment_movements_user_category":           repo.CollInvestmentMovements,
	} {
		raw, err := db.Collection(coll).Indexes().List(ctx)
		require.NoError(t, err)
		var all []bson.M
		require.NoError(t, raw.All(ctx, &all))
		found := false
		for _, spec := range all {
			if spec["name"] == name {
				found = true
				assert.NotNil(t, spec["partialFilterExpression"], "%s is partial", name)
			}
		}
		assert.True(t, found, name)
	}

	occurrences := indexSpecs(t, db, repo.CollTransactions)["transactions_recurrence_occurrence_unique"]
	require.NotNil(t, occurrences.Unique)
	assert.True(t, *occurrences.Unique, "one occurrence per rule day")

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

func TestApplyDropsObsoleteIndexes(t *testing.T) {
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(testutil.StartMongo(t)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	db := client.Database("monthra_test")

	// A database from before the *_date_id indexes.
	_, err = db.Collection(repo.CollTransactions).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "account_id", Value: 1}, {Key: "date", Value: -1}}, Options: options.Index().SetName("transactions_user_account_date")},
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "category_id", Value: 1}, {Key: "date", Value: -1}}, Options: options.Index().SetName("transactions_user_category_date")},
	})
	require.NoError(t, err)
	_, err = db.Collection(repo.CollRecurrences).Indexes().CreateOne(ctx,
		mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}}, Options: options.Index().SetName("recurrences_user")})
	require.NoError(t, err)
	_, err = db.Collection(repo.CollInvestmentMovements).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "investment_id", Value: 1}, {Key: "date", Value: -1}},
		Options: options.Index().SetName("investment_movements_user_investment_date"),
	})
	require.NoError(t, err)

	require.NoError(t, Apply(ctx, db, Options{AuditLogTTL: time.Hour}))
	assert.NotContains(t, indexSpecs(t, db, repo.CollRecurrences), "recurrences_user")
	movements := indexSpecs(t, db, repo.CollInvestmentMovements)
	assert.NotContains(t, movements, "investment_movements_user_investment_date")
	assert.Contains(t, movements, "investment_movements_user_investment_date_id")
	specs := indexSpecs(t, db, repo.CollTransactions)
	assert.NotContains(t, specs, "transactions_user_account_date")
	assert.NotContains(t, specs, "transactions_user_category_date")
	assert.Contains(t, specs, "transactions_user_account_date_id")
	var keys bson.D
	require.NoError(t, bson.Unmarshal(specs["transactions_user_account_date_id"].KeysDocument, &keys))
	assert.Equal(t,
		bson.D{{Key: "user_id", Value: int32(1)}, {Key: "account_id", Value: int32(1)}, {Key: "date", Value: int32(-1)}, {Key: "_id", Value: int32(-1)}},
		keys, "the listing order")
}
