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

	// InsertInstallments stores the installments of one purchase under a new
	// group id and returns them, in order.
	InsertInstallments(ctx context.Context, ts []domain.Transaction) ([]domain.Transaction, error)
	// Installments returns the installments of group groupID of userID
	// numbered from on (1 for all of them), in their order.
	Installments(ctx context.Context, userID, groupID string, from int) ([]domain.Transaction, error)
	// UpdateMany stores the editable fields of each of ts, as Update does.
	UpdateMany(ctx context.Context, ts []domain.Transaction) error
	// DeleteInstallments removes the installments of group groupID of
	// userID numbered from on (1 for all of them).
	DeleteInstallments(ctx context.Context, userID, groupID string, from int) error

	// Occurrences returns the occurrences of recurrence recurrenceID of
	// userID whose rule day is from or later (zero for all of them), in the
	// order of those days.
	Occurrences(ctx context.Context, userID, recurrenceID string, from domain.Date) ([]domain.Transaction, error)
	// DeleteOccurrences removes the occurrences of recurrence recurrenceID
	// of userID whose rule day is from or later (zero for all of them).
	DeleteOccurrences(ctx context.Context, userID, recurrenceID string, from domain.Date) error
}

// Series is what the transactions need from the recurrences: creating the
// occurrences due before each read, and keeping a recurrence in step with
// the scoped edits and deletions of its occurrences.
type Series interface {
	// Materialize creates the occurrences of userID's recurrences due up to
	// asOf (the reader's today; zero is today in UTC). It never fails.
	Materialize(ctx context.Context, userID string, asOf domain.Date)
	// Follow applies to the recurrence of an occurrence the changes a
	// scoped edit made to it, from before to after.
	Follow(ctx context.Context, before, after domain.Transaction) error
	// EndBefore ends the recurrence of an occurrence before its rule day.
	EndBefore(ctx context.Context, occurrence domain.Transaction) error
	// Remove deletes the recurrence of an occurrence.
	Remove(ctx context.Context, occurrence domain.Transaction) error
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
