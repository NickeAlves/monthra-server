// Package reports implements the reports of a user (§1 item 8): the income
// and expenses of a period by currency (by month, category, account and
// tag, beside the period before) with the net worth month by month, and
// the export of the transactions. PDFs are rendered by the app from the
// report. Every method takes the caller's user id from the access token,
// never from the body.
package reports

import (
	"context"

	"github.com/NickeAlves/monthra-server/internal/audit"
	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Accounts lists the caller's accounts.
type Accounts interface {
	List(ctx context.Context, userID string, includeArchived bool) ([]domain.Account, error)
}

// Categories lists the caller's categories (every kind for an empty one).
type Categories interface {
	List(ctx context.Context, userID string, kind domain.CategoryKind) ([]domain.Category, error)
}

// Ledger reads and sums the caller's transactions.
type Ledger interface {
	// Flows sums the expenses and income from day from to day to by month,
	// currency, kind and category.
	Flows(ctx context.Context, userID string, from, to domain.Date) ([]domain.FlowSum, error)
	// AccountFlows sums them by currency, kind and account.
	AccountFlows(ctx context.Context, userID string, from, to domain.Date) ([]domain.AccountFlowSum, error)
	// TagFlows sums them by currency, kind and tag.
	TagFlows(ctx context.Context, userID string, from, to domain.Date) ([]domain.TagFlowSum, error)
	// LegSums sums what the transactions up to day through moved on each
	// account, by month from month from on and in one sum before it.
	LegSums(ctx context.Context, userID string, from domain.Month, through domain.Date) ([]domain.LegSum, error)
	// List returns up to limit transactions of userID matching f, newest
	// date first and then newest id, after the cursor when there is one.
	List(ctx context.Context, userID string, f domain.TransactionFilter, after *domain.TransactionCursor, limit int) ([]domain.Transaction, error)
}

// Recurrences creates the occurrences due before the transactions are read.
type Recurrences interface {
	// Materialize creates the occurrences of userID's recurrences due up to
	// asOf. It never fails.
	Materialize(ctx context.Context, userID string, asOf domain.Date)
}

// Investments sums what the caller's investments paid, which counts as
// income (as on the dashboard) but in no account.
type Investments interface {
	IncomeFlows(ctx context.Context, userID string, from, to domain.Date) ([]domain.FlowSum, error)
}

// Auditor records the exports (§5.3). It never fails.
type Auditor interface {
	Record(ctx context.Context, e audit.Entry)
}

// Metrics counts the exports.
type Metrics interface {
	Exported(dataset string)
}
