package app

import (
	"log/slog"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/NickeAlves/monthra-server/internal/http/handler"
	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
	"github.com/NickeAlves/monthra-server/internal/usecase/accounts"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
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
func newFeatures(db *mongo.Database, logger *slog.Logger) features {
	accountsService := accounts.NewService(repomongo.NewAccounts(db), accounts.Options{})
	return features{
		routes: []handler.Routes{
			&handler.Accounts{Service: accountsService, Logger: logger},
		},
		erasers: []authuc.UserDataEraser{
			repomongo.NewUserEraser(db, repomongo.CollAccounts),
		},
	}
}
