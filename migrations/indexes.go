// Package migrations creates the MongoDB indexes at boot. Index creation is
// idempotent, so every instance can run it on start.
package migrations

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

// AuditLogTTLIndex is the name of the index that expires audit log entries.
const AuditLogTTLIndex = "audit_logs_ttl"

// Options configures Apply.
type Options struct {
	// AuditLogTTL is how long audit log entries are kept.
	AuditLogTTL time.Duration
}

// Indexes returns every index by collection. Names are explicit so they stay
// stable and readable in mongosh.
func Indexes() map[string][]mongo.IndexModel {
	return map[string][]mongo.IndexModel{
		repo.CollUsers: {
			// Emails are stored lowercase, so a plain unique index is case-insensitive in practice.
			{Keys: bson.D{{Key: "email", Value: 1}}, Options: options.Index().SetName("users_email_unique").SetUnique(true)},
		},
		repo.CollAccounts: {
			{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: 1}}, Options: options.Index().SetName("accounts_user_created")},
		},
		repo.CollCategories: {
			{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "kind", Value: 1}}, Options: options.Index().SetName("categories_user_kind")},
		},
		repo.CollTransactions: {
			// _id breaks ties between transactions on the same date for cursor pagination.
			{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "date", Value: -1}, {Key: "_id", Value: -1}}, Options: options.Index().SetName("transactions_user_date")},
			{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "account_id", Value: 1}, {Key: "date", Value: -1}}, Options: options.Index().SetName("transactions_user_account_date")},
			{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "category_id", Value: 1}, {Key: "date", Value: -1}}, Options: options.Index().SetName("transactions_user_category_date")},
		},
		repo.CollRecurrences: {
			{Keys: bson.D{{Key: "user_id", Value: 1}}, Options: options.Index().SetName("recurrences_user")},
		},
		repo.CollBudgets: {
			{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "month", Value: 1}, {Key: "category_id", Value: 1}}, Options: options.Index().SetName("budgets_user_month_category_unique").SetUnique(true)},
		},
		repo.CollInvestments: {
			{Keys: bson.D{{Key: "user_id", Value: 1}}, Options: options.Index().SetName("investments_user")},
		},
		repo.CollInvestmentMovements: {
			{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "investment_id", Value: 1}, {Key: "date", Value: -1}}, Options: options.Index().SetName("investment_movements_user_investment_date")},
		},
		repo.CollAuditLogs: {
			{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}, Options: options.Index().SetName("audit_logs_user_created")},
		},
		// Session state expires on its own: each document carries its expiry
		// in expires_at, so these TTL indexes use expireAfterSeconds=0.
		repo.CollSessions: {
			{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}}, Options: options.Index().SetName("sessions_user_created")},
			expiresAt("sessions_ttl"),
		},
		repo.CollRefreshTokens: {
			{Keys: bson.D{{Key: "token_hash", Value: 1}}, Options: options.Index().SetName("refresh_tokens_hash_unique").SetUnique(true)},
			{Keys: bson.D{{Key: "session_id", Value: 1}}, Options: options.Index().SetName("refresh_tokens_session")},
			expiresAt("refresh_tokens_ttl"),
		},
		repo.CollAuthTokens: {
			{Keys: bson.D{{Key: "token_hash", Value: 1}}, Options: options.Index().SetName("auth_tokens_hash_unique").SetUnique(true)},
			{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "purpose", Value: 1}}, Options: options.Index().SetName("auth_tokens_user_purpose")},
			expiresAt("auth_tokens_ttl"),
		},
	}
}

// expiresAt is a TTL index that deletes a document once its expires_at passes.
func expiresAt(name string) mongo.IndexModel {
	return mongo.IndexModel{
		Keys:    bson.D{{Key: "expires_at", Value: 1}},
		Options: options.Index().SetName(name).SetExpireAfterSeconds(0),
	}
}

// Apply creates every index, including the audit log TTL index. Re-running it
// with the same definitions is a no-op.
func Apply(ctx context.Context, db *mongo.Database, opts Options) error {
	for coll, models := range Indexes() {
		if _, err := db.Collection(coll).Indexes().CreateMany(ctx, models); err != nil {
			return fmt.Errorf("create indexes on %s: %w", coll, err)
		}
	}
	return ensureTTL(ctx, db.Collection(repo.CollAuditLogs), AuditLogTTLIndex, "created_at", opts.AuditLogTTL)
}

// ensureTTL creates a TTL index or, when the configured TTL changed, recreates it.
// collMod would avoid the drop, but it needs dbAdmin and the app user only has readWrite.
func ensureTTL(ctx context.Context, coll *mongo.Collection, name, field string, ttl time.Duration) error {
	whole := int64(ttl / time.Second)
	if whole <= 0 || whole > math.MaxInt32 {
		return errors.New("TTL must be between one second and ~68 years")
	}
	seconds := int32(whole)

	current, found, err := ttlSeconds(ctx, coll, name)
	if err != nil {
		return err
	}
	if found && current == seconds {
		return nil
	}
	if found {
		if err := coll.Indexes().DropOne(ctx, name); err != nil {
			return fmt.Errorf("drop outdated TTL index %s: %w", name, err)
		}
	}
	model := mongo.IndexModel{
		Keys:    bson.D{{Key: field, Value: 1}},
		Options: options.Index().SetName(name).SetExpireAfterSeconds(seconds),
	}
	if _, err := coll.Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("create TTL index %s: %w", name, err)
	}
	return nil
}

func ttlSeconds(ctx context.Context, coll *mongo.Collection, name string) (int32, bool, error) {
	specs, err := coll.Indexes().ListSpecifications(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("list indexes on %s: %w", coll.Name(), err)
	}
	for _, spec := range specs {
		if spec.Name == name {
			if spec.ExpireAfterSeconds == nil {
				return 0, true, nil
			}
			return *spec.ExpireAfterSeconds, true, nil
		}
	}
	return 0, false, nil
}
