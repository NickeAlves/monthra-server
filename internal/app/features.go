package app

import (
	"log/slog"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/NickeAlves/monthra-server/internal/http/handler"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
)

// features is what the domain features (accounts, categories, ...) add to
// the app: their routes, mounted on the authenticated group, and the erasers
// of their data, run by DELETE /me ahead of the auth ones.
type features struct {
	routes  []handler.Routes
	erasers []authuc.UserDataEraser
}

// newFeatures wires every domain feature on db.
func newFeatures(db *mongo.Database, logger *slog.Logger) features {
	_, _ = db, logger
	return features{}
}
