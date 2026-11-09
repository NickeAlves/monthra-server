// Package recurrences implements the recurring transactions of a user (§1
// items 1 and 2): rent, subscriptions, a salary or a monthly saving, and
// the occurrences they create when they fall due. Every method takes the
// caller's user id from the access token, never from the body.
package recurrences

import (
	"context"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Store keeps the recurrences. Every method is scoped to one user: a
// record of another user answers domain.ErrNotFound, like one that does
// not exist.
type Store interface {
	// List returns the recurrences of userID, oldest first.
	List(ctx context.Context, userID string) ([]domain.Recurrence, error)
	// Due returns up to limit active recurrences of userID whose next
	// occurrence falls on or before through, the earliest first.
	Due(ctx context.Context, userID string, through domain.Date, limit int) ([]domain.Recurrence, error)
	Count(ctx context.Context, userID string) (int, error)
	Insert(ctx context.Context, r domain.Recurrence) (domain.Recurrence, error)
	ByID(ctx context.Context, userID, id string) (domain.Recurrence, error)
	// Update stores the template, the rule and the next occurrence of r,
	// matching r.ID and r.UserID.
	Update(ctx context.Context, r domain.Recurrence) error
	// Advance records the occurrences of recurrence id created up to last,
	// the next one being next (zero once it ended), if its next occurrence
	// is still from; otherwise it changes nothing.
	Advance(ctx context.Context, userID, id string, from, next, last domain.Date) error
	Delete(ctx context.Context, userID, id string) error
	// AccountUsage reports whether a recurrence of userID names account id,
	// as its account or as a transfer's destination, and whether one of
	// those is still active.
	AccountUsage(ctx context.Context, userID, id string) (used, active bool, err error)
}

// Ledger stores the occurrences as transactions.
type Ledger interface {
	// InsertOccurrences stores the occurrences ts and returns how many were
	// new; one already stored for its recurrence and rule day is skipped.
	InsertOccurrences(ctx context.Context, ts []domain.Transaction) (int, error)
	// HasOccurrencesBefore reports whether recurrence recurrenceID of userID
	// has occurrences whose rule day comes before day.
	HasOccurrencesBefore(ctx context.Context, userID, recurrenceID string, day domain.Date) (bool, error)
}

// Accounts finds the caller's accounts a recurrence names.
type Accounts interface {
	ByID(ctx context.Context, userID, id string) (domain.Account, error)
	List(ctx context.Context, userID string, includeArchived bool) ([]domain.Account, error)
}

// Categories finds the caller's categories a recurrence names.
type Categories interface {
	ByID(ctx context.Context, userID, id string) (domain.Category, error)
}

// Metrics counts the transactions created.
type Metrics interface {
	TransactionCreated(kind string)
}

// Invalidator drops what is cached from a user's finance data (the
// dashboard summaries) after a write to it. It never fails.
type Invalidator interface {
	Invalidate(ctx context.Context, userID string)
}
