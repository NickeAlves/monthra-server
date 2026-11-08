// Package transactions implements the expenses, income and transfers of a
// user (§1 item 3). Every method takes the caller's user id from the access
// token, never from the body.
package transactions

import (
	"context"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Store keeps the transactions. Every method is scoped to one user: a
// record of another user answers domain.ErrNotFound, like one that does
// not exist.
type Store interface {
	Insert(ctx context.Context, t domain.Transaction) (domain.Transaction, error)
	ByID(ctx context.Context, userID, id string) (domain.Transaction, error)
	// Update stores the editable fields of t, matching t.ID and t.UserID.
	Update(ctx context.Context, t domain.Transaction) error
	Delete(ctx context.Context, userID, id string) error
	// List returns up to limit transactions of userID matching f, newest
	// date first and then newest id, after the cursor when there is one.
	List(ctx context.Context, userID string, f domain.TransactionFilter, after *domain.TransactionCursor, limit int) ([]domain.Transaction, error)
}

// Accounts finds the caller's accounts a transaction names.
type Accounts interface {
	ByID(ctx context.Context, userID, id string) (domain.Account, error)
}

// Categories finds the caller's categories a transaction names.
type Categories interface {
	ByID(ctx context.Context, userID, id string) (domain.Category, error)
}

// Metrics counts the transactions created.
type Metrics interface {
	TransactionCreated(kind string)
}
