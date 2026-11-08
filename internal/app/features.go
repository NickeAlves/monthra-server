package app

import (
	"log/slog"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/NickeAlves/monthra-server/internal/http/handler"
	"github.com/NickeAlves/monthra-server/internal/observability"
	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
	"github.com/NickeAlves/monthra-server/internal/usecase/accounts"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
	"github.com/NickeAlves/monthra-server/internal/usecase/categories"
	"github.com/NickeAlves/monthra-server/internal/usecase/recurrences"
	"github.com/NickeAlves/monthra-server/internal/usecase/transactions"
)

// features is what the domain features (accounts, categories, ...) add to
// the app: their routes, mounted on the authenticated group, and the erasers
// of their data, run by DELETE /me ahead of the auth ones.
type features struct {
	routes  []handler.Routes
	erasers []authuc.UserDataEraser
}

// newFeatures wires every domain feature on db. Each one that stores data by
// user_id registers its eraser (the UserDataEraser contract of CLAUDE.md).
func newFeatures(db *mongo.Database, logger *slog.Logger, metrics *observability.Metrics) features {
	accountsStore, categoriesStore := repomongo.NewAccounts(db), repomongo.NewCategories(db)
	// The transactions hold the balances of the accounts and tell whether
	// an account or a category is still in use.
	ledger := repomongo.NewTransactions(db)
	// The recurrences create their occurrences before every read of the
	// transactions (balances and invoices included), and name accounts and
	// categories too.
	recurrencesStore := repomongo.NewRecurrences(db)
	recurrencesService := recurrences.NewService(recurrencesStore, ledger, accountsStore, categoriesStore,
		recurrences.Options{Logger: logger, Metrics: metrics})
	accountsService := accounts.NewService(accountsStore, ledger, accounts.Options{Recurrences: recurrencesService})
	// users.categories_seeded_at marks who already got the default set.
	categoriesService := categories.NewService(categoriesStore, repomongo.NewUsers(db), ledger,
		categories.Options{Recurrences: recurrencesStore})
	transactionsService := transactions.NewService(ledger, accountsStore, categoriesStore,
		transactions.Options{Metrics: metrics, Series: recurrencesService})
	return features{
		routes: []handler.Routes{
			&handler.Accounts{Service: accountsService, Logger: logger},
			&handler.Categories{Service: categoriesService, Logger: logger},
			&handler.Transactions{Service: transactionsService, Logger: logger},
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
		},
	}
}
