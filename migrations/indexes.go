// Package migrations creates the MongoDB indexes at boot. Index creation is
// idempotent, so every instance can run it on start.
package migrations

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
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
			// One default category per key and user, so seeding twice (or
			// concurrently) never duplicates one; categories the owner created
			// have no default_key and stay out of it.
			{
				Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "default_key", Value: 1}},
				Options: options.Index().SetName("categories_user_default_key_unique").SetUnique(true).
					SetPartialFilterExpression(bson.D{{Key: "default_key", Value: bson.D{{Key: "$exists", Value: true}}}}),
			},
		},
		// Every listing sorts by {date: -1, _id: -1} (_id breaks the ties of
		// one day for cursor pagination), so each filter's index ends the
		// same way and a page reads only its own entries.
		repo.CollTransactions: {
			transactionsBy("transactions_user_date"),
			transactionsBy("transactions_user_account_date_id", "account_id"),
			// The destination of transfers: account statements and balances.
			transactionsBy("transactions_user_to_account_date_id", "to_account_id"),
			transactionsBy("transactions_user_category_date_id", "category_id"),
			transactionsBy("transactions_user_tags_date_id", "tags"),
			// The legs on a credit card, by invoice: an invoice's statement
			// and the sums of a card's invoices.
			transactionsByInvoice("transactions_user_account_invoice_date_id", "account_id", "invoice_month"),
			transactionsByInvoice("transactions_user_to_account_invoice_date_id", "to_account_id", "to_invoice_month"),
			// The installments of a purchase: their listing and the edits and
			// deletions of a group, which read at most MaxInstallments entries.
			partial(transactionsBy("transactions_user_installment_date_id", "installment.group_id"), "installment.group_id"),
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

// obsolete lists, by collection, the indexes an earlier version created and
// a later one replaced; Apply drops them where they still exist, so an old
// database never keeps a stale index or clashes with a new definition.
var obsolete = map[string][]string{
	// Replaced by the *_date_id indexes, which match the listing order.
	repo.CollTransactions: {"transactions_user_account_date", "transactions_user_category_date"},
}

// transactionsBy is an index of the transactions of a user by the fields
// given, in the listing order.
func transactionsBy(name string, fields ...string) mongo.IndexModel {
	keys := bson.D{{Key: "user_id", Value: 1}}
	for _, f := range fields {
		keys = append(keys, bson.E{Key: f, Value: 1})
	}
	keys = append(keys, bson.E{Key: "date", Value: -1}, bson.E{Key: "_id", Value: -1})
	return mongo.IndexModel{Keys: keys, Options: options.Index().SetName(name)}
}

// transactionsByInvoice is an index of the card legs of the transactions of
// a user, by account and invoice, in the listing order. It is partial: only
// a leg on a credit card has an invoice, and the store never writes a null
// one.
func transactionsByInvoice(name, account, invoice string) mongo.IndexModel {
	return partial(transactionsBy(name, account, invoice), invoice)
}

// partial limits index m to the documents that hold field.
func partial(m mongo.IndexModel, field string) mongo.IndexModel {
	m.Options.SetPartialFilterExpression(bson.D{{Key: field, Value: bson.D{{Key: "$exists", Value: true}}}})
	return m
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
	for coll, names := range obsolete {
		if err := dropIndexes(ctx, db.Collection(coll), names); err != nil {
			return err
		}
	}
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

// dropIndexes drops the indexes of names that exist on coll.
func dropIndexes(ctx context.Context, coll *mongo.Collection, names []string) error {
	specs, err := coll.Indexes().ListSpecifications(ctx)
	if err != nil {
		return fmt.Errorf("list indexes on %s: %w", coll.Name(), err)
	}
	for _, spec := range specs {
		if slices.Contains(names, spec.Name) {
			if err := coll.Indexes().DropOne(ctx, spec.Name); err != nil {
				return fmt.Errorf("drop obsolete index %s: %w", spec.Name, err)
			}
		}
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
