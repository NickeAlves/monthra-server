// Package dashboard implements the monthly dashboard of a user (§1 item 2):
// income and expenses of a month, by category and by currency, what the
// recurrences are still to create in it, the balances and a 12-month
// history of the net worth. Summaries are cached (cache-aside, §5.4) under
// a version of the user's data, which every write bumps (Invalidator).
// Every method takes the caller's user id from the access token, never
// from the body.
package dashboard

import (
	"context"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Accounts lists the caller's accounts.
type Accounts interface {
	List(ctx context.Context, userID string, includeArchived bool) ([]domain.Account, error)
}

// Ledger sums the caller's transactions.
type Ledger interface {
	// Flows sums the expenses and income from day from to day to by month,
	// currency, kind and category.
	Flows(ctx context.Context, userID string, from, to domain.Date) ([]domain.FlowSum, error)
	// LegSums sums what the transactions up to day through moved on each
	// account, by month from month from on and in one sum before it.
	LegSums(ctx context.Context, userID string, from domain.Month, through domain.Date) ([]domain.LegSum, error)
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

// Cache keeps the summaries of each user under a version of their data.
type Cache interface {
	// Version returns the current version of userID's summaries, starting
	// one when there is none.
	Version(ctx context.Context, userID string) (string, error)
	// Get returns the summary of userID at slot under version, or false.
	Get(ctx context.Context, userID, version, slot string) ([]byte, bool, error)
	// Set caches the summary of userID at slot under version.
	Set(ctx context.Context, userID, version, slot string, value []byte) error
	// Bump starts a new version of userID's summaries.
	Bump(ctx context.Context, userID string) error
}

// Metrics counts the cache lookups and failures.
type Metrics interface {
	CacheRequest(cache, result string)
	CacheError(cache, op string)
}
