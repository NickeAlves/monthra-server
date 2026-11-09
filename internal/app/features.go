package app

import (
	"context"
	"log/slog"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/http/handler"
	"github.com/NickeAlves/monthra-server/internal/observability"
	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
	"github.com/NickeAlves/monthra-server/internal/usecase/accounts"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
	"github.com/NickeAlves/monthra-server/internal/usecase/budgets"
	"github.com/NickeAlves/monthra-server/internal/usecase/categories"
	"github.com/NickeAlves/monthra-server/internal/usecase/dashboard"
	"github.com/NickeAlves/monthra-server/internal/usecase/investments"
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
	budgets      *budgets.Service
	investments  *investments.Service
}

// featureDeps is what the domain features are built on.
type featureDeps struct {
	db      *mongo.Database
	redis   *redis.Client
	keys    cache.Keys
	logger  *slog.Logger
	metrics *observability.Metrics
	// mailer sends the budget alerts, with links to appURL.
	mailer budgets.Mailer
	appURL config.PublicURL
}

// newFeatures wires every domain feature on d.db, the dashboard summaries
// cached in d.redis. Each one that stores data by user_id registers its
// eraser (the UserDataEraser contract of CLAUDE.md).
func newFeatures(d featureDeps) features {
	db, logger, metrics := d.db, d.logger, d.metrics
	accountsStore, categoriesStore := repomongo.NewAccounts(db), repomongo.NewCategories(db)
	users := repomongo.NewUsers(db)
	// The transactions hold the balances of the accounts and tell whether
	// an account or a category is still in use.
	ledger := repomongo.NewTransactions(db)
	// Every write to a user's finance data drops their cached summaries and
	// checks their budget alerts, so the hooks come before the use cases
	// that write. users.budgets_rolled_through marks the months whose
	// budgets were carried over.
	summaries := cache.NewSummaries(d.redis, d.keys, 0)
	invalidator := dashboard.NewInvalidator(summaries, dashboard.Options{Logger: logger, Metrics: metrics})
	budgetsStore := repomongo.NewBudgets(db)
	alerts := budgets.NewAlerts(budgetsStore, users, ledger, users, d.mailer,
		budgets.AlertsOptions{Logger: logger, Metrics: metrics, AppURL: d.appURL})
	hooks := writeHooks{invalidator.Invalidate, alerts.Check}
	// The recurrences create their occurrences before every read of the
	// transactions (balances and invoices included), and name accounts and
	// categories too.
	recurrencesStore := repomongo.NewRecurrences(db)
	recurrencesService := recurrences.NewService(recurrencesStore, ledger, accountsStore, categoriesStore,
		recurrences.Options{Logger: logger, Metrics: metrics, AfterWrite: hooks})
	accountsService := accounts.NewService(accountsStore, ledger,
		accounts.Options{Recurrences: recurrencesService, AfterWrite: hooks})
	// users.categories_seeded_at marks who already got the default set.
	categoriesService := categories.NewService(categoriesStore, users, ledger,
		categories.Options{Recurrences: recurrencesStore, Budgets: budgetsStore, AfterWrite: hooks})
	transactionsService := transactions.NewService(ledger, accountsStore, categoriesStore,
		transactions.Options{Metrics: metrics, Series: recurrencesService, AfterWrite: hooks})
	dashboardService := dashboard.NewService(accountsStore, ledger, recurrencesService, summaries,
		dashboard.Options{Logger: logger, Metrics: metrics})
	// The budgets read what was spent after the recurrences create their
	// occurrences, so they come after them (the alerts came before).
	budgetsService := budgets.NewService(budgetsStore, users, categoriesStore, ledger, recurrencesService, budgets.Options{})
	// The portfolio stands apart from the accounts; only its income counts
	// on the dashboard.
	investmentsStore, movementsStore := repomongo.NewInvestments(db), repomongo.NewInvestmentMovements(db)
	investmentsService := investments.NewService(investmentsStore, movementsStore, accountsStore, categoriesStore,
		investments.Options{AfterWrite: hooks, Metrics: metrics})
	return features{
		routes: []handler.Routes{
			&handler.Accounts{Service: accountsService, Logger: logger},
			&handler.Categories{Service: categoriesService, Logger: logger},
			&handler.Transactions{Service: transactionsService, Logger: logger},
			&handler.Recurrences{Service: recurrencesService, Logger: logger},
			&handler.Dashboard{Service: dashboardService, Logger: logger},
			&handler.Budgets{Service: budgetsService, Logger: logger},
			&handler.Investments{Service: investmentsService, Logger: logger},
		},
		// Recurrences first, so no read during the deletion creates
		// occurrences again; then transactions: a deletion interrupted
		// halfway never leaves records naming accounts or categories that
		// are gone.
		erasers: []authuc.UserDataEraser{
			repomongo.NewUserEraser(db, repomongo.CollRecurrences),
			repomongo.NewUserEraser(db, repomongo.CollTransactions),
			// Budgets before the categories they name; the movements (whose
			// income names categories) before their investments (which name
			// accounts).
			repomongo.NewUserEraser(db, repomongo.CollBudgets),
			repomongo.NewUserEraser(db, repomongo.CollInvestmentMovements),
			repomongo.NewUserEraser(db, repomongo.CollInvestments),
			repomongo.NewUserEraser(db, repomongo.CollAccounts),
			repomongo.NewUserEraser(db, repomongo.CollCategories),
			// The summaries cached under the dropped version are never read
			// again and expire within cache.DefaultSummaryTTL.
			summaries,
		},
		services: featureServices{
			accounts: accountsService, categories: categoriesService,
			transactions: transactionsService, recurrences: recurrencesService, budgets: budgetsService,
			investments: investmentsService,
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
