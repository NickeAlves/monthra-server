// Package mongo holds the MongoDB connection and, as features land, the
// repository implementations backed by it.
package mongo

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/NickeAlves/monthra-server/internal/config"
)

// Collection names.
const (
	CollUsers               = "users"
	CollAccounts            = "accounts"
	CollCategories          = "categories"
	CollTransactions        = "transactions"
	CollRecurrences         = "recurrences"
	CollBudgets             = "budgets"
	CollInvestments         = "investments"
	CollInvestmentMovements = "investment_movements"
	CollAuditLogs           = "audit_logs"
	CollSessions            = "sessions"
	CollRefreshTokens       = "refresh_tokens"
	CollAuthTokens          = "auth_tokens" //nolint:gosec // G101: a collection name, not a credential
)

// Connect opens a traced client and pings the primary, so the service fails
// fast at boot when MongoDB is unreachable or the credentials are wrong.
func Connect(ctx context.Context, cfg config.Mongo, appName string) (*mongo.Client, error) {
	opts := options.Client().
		ApplyURI(cfg.URI).
		SetAppName(appName).
		SetConnectTimeout(cfg.ConnectTimeout).
		SetServerSelectionTimeout(cfg.ConnectTimeout).
		SetMaxPoolSize(cfg.MaxPoolSize).
		SetMonitor(NewCommandMonitor())

	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("connect to mongo: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("ping mongo: %w", err)
	}
	return client, nil
}

// Pinger adapts a client to the readiness probe.
type Pinger struct{ Client *mongo.Client }

// Ping checks that the primary answers.
func (p Pinger) Ping(ctx context.Context) error {
	return p.Client.Ping(ctx, readpref.Primary())
}
