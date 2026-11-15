package dataexport

import (
	"context"
	"fmt"
	"iter"

	"github.com/NickeAlves/monthra-server/internal/audit"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// TransactionsPageSize is how many transactions one read takes, so the
// export holds a page at a time whatever the history.
const TransactionsPageSize = 500

// Users reads the account.
type Users interface {
	ByID(ctx context.Context, id string) (domain.User, error)
}

// Accounts lists the accounts, the archived ones included.
type Accounts interface {
	List(ctx context.Context, userID string, includeArchived bool) ([]domain.Account, error)
}

// Categories lists the categories (every kind for an empty one).
type Categories interface {
	List(ctx context.Context, userID string, kind domain.CategoryKind) ([]domain.Category, error)
}

// Ledger lists the transactions with the list's cursor.
type Ledger interface {
	List(ctx context.Context, userID string, f domain.TransactionFilter, after *domain.TransactionCursor, limit int) ([]domain.Transaction, error)
}

// Materializer creates the occurrences of the recurrences due up to asOf
// (UTC today for a zero one). It never fails.
type Materializer interface {
	Materialize(ctx context.Context, userID string, asOf domain.Date)
}

// Recurrences lists the recurrences.
type Recurrences interface {
	List(ctx context.Context, userID string) ([]domain.Recurrence, error)
}

// Budgets yields the budgets of every month.
type Budgets interface {
	All(ctx context.Context, userID string) iter.Seq2[domain.Budget, error]
}

// Investments lists the investments.
type Investments interface {
	List(ctx context.Context, userID string) ([]domain.Investment, error)
}

// Movements lists the movements of one investment.
type Movements interface {
	Of(ctx context.Context, userID, investmentID string) ([]domain.Movement, error)
}

// Sessions yields every stored session.
type Sessions interface {
	ForUser(ctx context.Context, userID string) iter.Seq2[domain.Session, error]
}

// AuditLog yields the audit entries kept.
type AuditLog interface {
	ForUser(ctx context.Context, userID string) iter.Seq2[audit.Entry, error]
}

// Profile is profile.json: the account as GET /me answers it (preferences
// included; no hash, secret or code).
func Profile(users Users) Object {
	return Object{Name: FileProfile, Read: func(ctx context.Context, userID string) (any, error) {
		u, err := users.ByID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("find user: %w", err)
		}
		return dto.NewMeResponse(u), nil
	}}
}

// AccountsFile is accounts.json, the archived ones included.
func AccountsFile(accounts Accounts) List {
	return List{Name: FileAccounts, Records: func(ctx context.Context, userID string) iter.Seq2[any, error] {
		list, err := accounts.List(ctx, userID, true)
		return all(list, err, func(a domain.Account) any { return dto.NewAccountRecord(a) })
	}}
}

// CategoriesFile is categories.json, a default category named in the
// stored locale until renamed, as GET /categories answers.
func CategoriesFile(categories Categories) List {
	return List{Name: FileCategories, Records: func(ctx context.Context, userID string) iter.Seq2[any, error] {
		l := i18n.FromContext(ctx)
		list, err := categories.List(ctx, userID, "")
		return all(list, err, func(c domain.Category) any { return dto.NewCategory(c, l) })
	}}
}

// TransactionsFile is transactions.json: every transaction, newest first,
// after the occurrences due are created, so it matches what the app shows.
func TransactionsFile(ledger Ledger, recurrences Materializer) List {
	return List{Name: FileTransactions, Records: func(ctx context.Context, userID string) iter.Seq2[any, error] {
		recurrences.Materialize(ctx, userID, domain.Date{})
		return each(transactions(ctx, ledger, userID), func(t domain.Transaction) any { return dto.NewTransaction(t) })
	}}
}

// transactions pages through every transaction of userID with the list's
// cursor, which bounds the date, so each page reads its own entries.
func transactions(ctx context.Context, ledger Ledger, userID string) iter.Seq2[domain.Transaction, error] {
	return func(yield func(domain.Transaction, error) bool) {
		var after *domain.TransactionCursor
		for {
			page, err := ledger.List(ctx, userID, domain.TransactionFilter{}, after, TransactionsPageSize)
			if err != nil {
				yield(domain.Transaction{}, fmt.Errorf("list transactions: %w", err))
				return
			}
			for _, t := range page {
				if !yield(t, nil) {
					return
				}
			}
			if len(page) < TransactionsPageSize {
				return
			}
			last := page[len(page)-1]
			after = &domain.TransactionCursor{Date: last.Date, ID: last.ID}
		}
	}
}

// RecurrencesFile is recurrences.json, the ended ones included.
func RecurrencesFile(recurrences Recurrences) List {
	return List{Name: FileRecurrences, Records: func(ctx context.Context, userID string) iter.Seq2[any, error] {
		list, err := recurrences.List(ctx, userID)
		return all(list, err, func(r domain.Recurrence) any { return dto.NewRecurrence(r) })
	}}
}

// BudgetsFile is budgets.json: every month's budgets, oldest first, without
// their usage.
func BudgetsFile(budgets Budgets) List {
	return List{Name: FileBudgets, Records: func(ctx context.Context, userID string) iter.Seq2[any, error] {
		return each(budgets.All(ctx, userID), func(b domain.Budget) any { return dto.NewBudgetRecord(b) })
	}}
}

// InvestmentsFile is investments.json, without the positions.
func InvestmentsFile(investments Investments) List {
	return List{Name: FileInvestments, Records: func(ctx context.Context, userID string) iter.Seq2[any, error] {
		list, err := investments.List(ctx, userID)
		return all(list, err, func(inv domain.Investment) any { return dto.NewInvestmentRecord(inv) })
	}}
}

// MovementsFile is investment-movements.json: the movements of each
// investment in the order of investments.json, each one's newest first.
func MovementsFile(investments Investments, movements Movements) List {
	return List{Name: FileInvestmentMovements, Records: func(ctx context.Context, userID string) iter.Seq2[any, error] {
		return func(yield func(any, error) bool) {
			list, err := investments.List(ctx, userID)
			if err != nil {
				yield(nil, fmt.Errorf("list investments: %w", err))
				return
			}
			for _, inv := range list {
				ms, err := movements.Of(ctx, userID, inv.ID)
				if err != nil {
					yield(nil, fmt.Errorf("list movements: %w", err))
					return
				}
				for _, m := range ms {
					if !yield(dto.NewMovement(m), nil) {
						return
					}
				}
			}
		}
	}}
}

// SessionsFile is sessions.json: every stored session, newest first.
func SessionsFile(sessions Sessions) List {
	return List{Name: FileSessions, Records: func(ctx context.Context, userID string) iter.Seq2[any, error] {
		return each(sessions.ForUser(ctx, userID), func(s domain.Session) any { return dto.NewSessionRecord(s) })
	}}
}

// AuditLogFile is audit-log.json: the security events kept, newest first.
func AuditLogFile(log AuditLog) List {
	return List{Name: FileAuditLog, Records: func(ctx context.Context, userID string) iter.Seq2[any, error] {
		return each(log.ForUser(ctx, userID), func(e audit.Entry) any { return dto.NewAuditEntry(e) })
	}}
}
