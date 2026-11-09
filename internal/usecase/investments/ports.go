// Package investments implements the caller's portfolio (§1 item 7): the
// assets, their movements (buys, sales, income, fees, valuations) and the
// position each one makes, with its return, and the totals by currency.
// The portfolio stands apart from the accounts: a movement never changes an
// account's balance; only the income counts on the dashboard, as income of
// its month. Every method takes the caller's user id from the access token,
// never from the body.
package investments

import (
	"context"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Store keeps the investments. Every method filters by userID, so another
// user's investment is domain.ErrNotFound.
type Store interface {
	// List returns the investments of userID, oldest first.
	List(ctx context.Context, userID string) ([]domain.Investment, error)
	Count(ctx context.Context, userID string) (int, error)
	Insert(ctx context.Context, inv domain.Investment) (domain.Investment, error)
	ByID(ctx context.Context, userID, id string) (domain.Investment, error)
	Update(ctx context.Context, inv domain.Investment) error
	Delete(ctx context.Context, userID, id string) error
}

// Movements keeps the movements of the investments. Every method filters
// by userID, and by the investment where it names one, so a movement is
// reached through its own investment only.
type Movements interface {
	// Of returns every movement of investment invID.
	Of(ctx context.Context, userID, invID string) ([]domain.Movement, error)
	// All returns every movement of userID by investment id.
	All(ctx context.Context, userID string) (map[string][]domain.Movement, error)
	// Page returns up to limit movements of investment invID matching f,
	// newest first, after the cursor when there is one.
	Page(ctx context.Context, userID, invID string, f domain.MovementFilter, after *domain.MovementCursor, limit int) ([]domain.Movement, error)
	ByID(ctx context.Context, userID, invID, id string) (domain.Movement, error)
	Count(ctx context.Context, userID, invID string) (int, error)
	Insert(ctx context.Context, m domain.Movement) (domain.Movement, error)
	Update(ctx context.Context, m domain.Movement) error
	Delete(ctx context.Context, userID, invID, id string) error
	// DeleteOf removes every movement of investment invID.
	DeleteOf(ctx context.Context, userID, invID string) error
}

// Accounts finds the caller's accounts, to check the one an investment is
// kept at.
type Accounts interface {
	ByID(ctx context.Context, userID, id string) (domain.Account, error)
}

// Categories finds the caller's categories, to check the one an income
// counts under.
type Categories interface {
	ByID(ctx context.Context, userID, id string) (domain.Category, error)
}

// WriteHook runs after every write attempt to a user's finance data (it
// drops the cached dashboard summaries and checks the budget alerts). It
// never fails.
type WriteHook interface {
	AfterWrite(ctx context.Context, userID string)
}

// Metrics counts the movements created, by type.
type Metrics interface {
	MovementCreated(movementType string)
}
