// Package categories implements the categories of a user (§1 item 5): the
// default set every user starts with, and the ones they create. Every method
// takes the caller's user id from the access token, never from the body.
package categories

import (
	"context"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Store keeps the categories. Every method is scoped to one user: a record
// of another user answers domain.ErrNotFound, like one that does not exist.
type Store interface {
	// List returns the categories of userID, oldest first; only those of
	// kind, unless kind is empty.
	List(ctx context.Context, userID string, kind domain.CategoryKind) ([]domain.Category, error)
	Count(ctx context.Context, userID string) (int, error)
	Insert(ctx context.Context, c domain.Category) (domain.Category, error)
	// InsertDefaults stores the default categories of list that their owner
	// does not hold yet: one with the same default key is kept as it is, so
	// concurrent or repeated calls never duplicate a default.
	InsertDefaults(ctx context.Context, list []domain.Category) error
	ByID(ctx context.Context, userID, id string) (domain.Category, error)
	// Update stores the editable fields of c, matching c.ID and c.UserID.
	Update(ctx context.Context, c domain.Category) error
	Delete(ctx context.Context, userID, id string) error
}

// SeedMarker records which users already got the default categories, so a
// default the owner deleted never comes back.
type SeedMarker interface {
	// CategoriesSeeded reports whether userID got the default set. A user
	// that does not exist counts as seeded: nothing is written for an
	// account being erased.
	CategoriesSeeded(ctx context.Context, userID string) (bool, error)
	// MarkCategoriesSeeded records that userID got the default set; marking
	// twice keeps the first date.
	MarkCategoriesSeeded(ctx context.Context, userID string, at time.Time) error
}

// Usage is what the categories need from the transactions.
type Usage interface {
	// CategoryInUse reports whether category id of userID has transactions.
	CategoryInUse(ctx context.Context, userID, id string) (bool, error)
	// ReassignCategory moves the transactions of userID from category from
	// to category to.
	ReassignCategory(ctx context.Context, userID, from, to string, at time.Time) error
}
