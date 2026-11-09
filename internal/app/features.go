package app

import (
	"context"
	"log/slog"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/http/handler"
	"github.com/NickeAlves/monthra-server/internal/observability"
	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
	"github.com/NickeAlves/monthra-server/internal/usecase/accounts"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
	"github.com/NickeAlves/monthra-server/internal/usecase/categories"
	"github.com/NickeAlves/monthra-server/internal/usecase/dashboard"
	"github.com/NickeAlves/monthra-server/internal/usecase/recurrences"
	"github.com/NickeAlves/monthra-server/internal/usecase/transactions"
)

// features is what the domain features (accounts, categories, ...) add to
// the app: their routes, mounted on the authenticated group, and the erasers
// of their data, run by DELETE /me ahead of the auth ones.
type features struct {
	routes  []handler.Routes
	erasers []authuc.UserDataEraser
	// services write the demo data of make seed through the same rules as
	// the API.
	services featureServices
}

// featureServices are the use cases of the features that store data.
type featureServices struct {
	accounts     *accounts.Service
	categories   *categories.Service
	transactions *transactions.Service
	recurrences  *recurrences.Service
}

// newFeatures wires every domain feature on db, the dashboard summaries
// cached in rdb. Each one that stores data by user_id registers its eraser
// (the UserDataEraser contract of CLAUDE.md).
func newFeatures(db *mongo.Database, rdb *redis.Client, keys cache.Keys, logger *slog.Logger, metrics *observability.Metrics) features {
	// Every write to a user's finance data drops their cached summaries, so
	// the hooks come before the use cases that write.
	summaries := cache.NewSummaries(rdb, keys, 0)
	invalidator := dashboard.NewInvalidator(summaries, dashboard.Options{Logger: logger, Metrics: metrics})
	hooks := writeHooks{invalidator.Invalidate}
	accountsStore, categoriesStore := repomongo.NewAccounts(db), repomongo.NewCategories(db)
	// The transactions hold the balances of the accounts and tell whether
	// an account or a category is still in use.
	ledger := repomongo.NewTransactions(db)
	// The recurrences create their occurrences before every read of the
	// transactions (balances and invoices included), and name accounts and
	// categories too.
	recurrencesStore := repomongo.NewRecurrences(db)
	recurrencesService := recurrences.NewService(recurrencesStore, ledger, accountsStore, categoriesStore,
		recurrences.Options{Logger: logger, Metrics: metrics, AfterWrite: hooks})
	accountsService := accounts.NewService(accountsStore, ledger,
		accounts.Options{Recurrences: recurrencesService, AfterWrite: hooks})
	// users.categories_seeded_at marks who already got the default set.
	categoriesService := categories.NewService(categoriesStore, repomongo.NewUsers(db), ledger,
		categories.Options{Recurrences: recurrencesStore, AfterWrite: hooks})
	transactionsService := transactions.NewService(ledger, accountsStore, categoriesStore,
		transactions.Options{Metrics: metrics, Series: recurrencesService, AfterWrite: hooks})
	dashboardService := dashboard.NewService(accountsStore, ledger, recurrencesService, summaries,
		dashboard.Options{Logger: logger, Metrics: metrics})
	return features{
		routes: []handler.Routes{
			&handler.Accounts{Service: accountsService, Logger: logger},
			&handler.Categories{Service: categoriesService, Logger: logger},
			&handler.Transactions{Service: transactionsService, Logger: logger},
			&handler.Recurrences{Service: recurrencesService, Logger: logger},
			&handler.Dashboard{Service: dashboardService, Logger: logger},
		},
		// Recurrences first, so no read during the deletion creates
		// occurrences again; then transactions: a deletion interrupted
		// halfway never leaves records naming accounts or categories that
		// are gone.
		erasers: []authuc.UserDataEraser{
			repomongo.NewUserEraser(db, repomongo.CollRecurrences),
			repomongo.NewUserEraser(db, repomongo.CollTransactions),
			repomongo.NewUserEraser(db, repomongo.CollAccounts),
			repomongo.NewUserEraser(db, repomongo.CollCategories),
			// The summaries cached under the dropped version are never read
			// again and expire within cache.DefaultSummaryTTL.
			summaries,
		},
		services: featureServices{
			accounts: accountsService, categories: categoriesService,
			transactions: transactionsService, recurrences: recurrencesService,
		},
	}
}

// writeHooks run, in order, after every write attempt to a user's finance
// data (the use cases' WriteHook). Each one never fails.
type writeHooks []func(ctx context.Context, userID string)

// AfterWrite runs every hook for userID.
func (h writeHooks) AfterWrite(ctx context.Context, userID string) {
	for _, run := range h {
		run(ctx, userID)
	}
}
