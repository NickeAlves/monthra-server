// Package budgets implements the monthly budgets by expense category (§1
// item 6): what each one spent and what the recurrences are still to spend
// in its month, its status, the carry-over of a month's budgets to the
// next, and the alert emails when spending crosses a budget's warning
// share or its limit (Alerts). Every method takes the caller's user id from
// the access token, never from the body.
package budgets

import (
	"context"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Store keeps the budgets. Every method filters by userID, so another
// user's budget is domain.ErrNotFound.
type Store interface {
	// List returns the budgets of userID in month, in creation order.
	List(ctx context.Context, userID string, month domain.Month) ([]domain.Budget, error)
	// Pending returns the budgets of userID in month not exceeded yet.
	Pending(ctx context.Context, userID string, month domain.Month) ([]domain.Budget, error)
	ByID(ctx context.Context, userID, id string) (domain.Budget, error)
	// Insert answers domain.ErrBudgetExists when the category already has a
	// budget in the month.
	Insert(ctx context.Context, b domain.Budget) (domain.Budget, error)
	// InsertRolled stores a month's carried-over budgets, skipping the
	// categories that already have one in it.
	InsertRolled(ctx context.Context, list []domain.Budget) error
	Update(ctx context.Context, b domain.Budget) error
	// ClaimAlert marks the alert of status as given and reports whether
	// this call marked it (once, under concurrent calls).
	ClaimAlert(ctx context.Context, userID, id string, status domain.BudgetStatus, at time.Time) (bool, error)
	Delete(ctx context.Context, userID, id string) error
}

// Marker records the latest month the caller's budgets were carried over
// to.
type Marker interface {
	// BudgetsRolledThrough returns that month, zero when never; a user that
	// is gone answers domain.MaxMonth.
	BudgetsRolledThrough(ctx context.Context, userID string) (domain.Month, error)
	// MarkBudgetsRolled moves the mark forward to m (never back).
	MarkBudgetsRolled(ctx context.Context, userID string, m domain.Month) error
}

// Categories finds the caller's categories, to check what a budget names.
type Categories interface {
	ByID(ctx context.Context, userID, id string) (domain.Category, error)
}

// Ledger sums the caller's transactions.
type Ledger interface {
	// Flows sums the expenses and income from day from to day to by month,
	// currency, kind and category.
	Flows(ctx context.Context, userID string, from, to domain.Date) ([]domain.FlowSum, error)
}

// Recurrences creates the occurrences due and projects the ones to come.
type Recurrences interface {
	// Materialize creates the occurrences of userID's recurrences due up to
	// asOf. It never fails.
	Materialize(ctx context.Context, userID string, asOf domain.Date)
	// Scheduled returns what userID's recurrences are still to create from
	// day from through day to.
	Scheduled(ctx context.Context, userID string, from, to domain.Date) ([]domain.Transaction, error)
}
