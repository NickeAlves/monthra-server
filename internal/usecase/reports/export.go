package reports

import (
	"context"
	"fmt"
	"iter"

	"github.com/NickeAlves/monthra-server/internal/audit"
	"github.com/NickeAlves/monthra-server/internal/domain"
)

const (
	// ExportPageSize is how many transactions an export reads at a time,
	// so its memory stays the same whatever it holds.
	ExportPageSize = 500
	// FormatCSV is the format of the exports; the app renders the PDFs.
	FormatCSV = "csv"
	// datasetTransactions labels the exports of transactions.
	datasetTransactions = "transactions"
)

// Requester is who asks for an export, as the audit log records it.
type Requester struct {
	UserID, SessionID string
	IP, UserAgent     string
}

// ExportQuery selects the transactions to export, as the list does, for a
// reader whose today is AsOf (domain.Today).
type ExportQuery struct {
	Filter domain.TransactionFilter
	AsOf   domain.Date
}

// TransactionExport is an export of transactions: what they name, by id,
// and the transactions themselves, read as Rows is ranged over.
type TransactionExport struct {
	Accounts   map[string]domain.Account
	Categories map[string]domain.Category
	// Rows yields the transactions in the order of the list (newest date
	// first, then newest id), a page of ExportPageSize at a time; a failed
	// read yields the error and ends.
	Rows iter.Seq2[domain.Transaction, error]
}

// ExportTransactions starts an export of the caller's transactions that
// match q.Filter (another user's ids match none) and records it in the
// audit log. The occurrences due are created first. Rows reads with ctx.
func (s *Service) ExportTransactions(ctx context.Context, r Requester, q ExportQuery) (TransactionExport, error) {
	s.recurrences.Materialize(ctx, r.UserID, q.AsOf)
	accounts, err := s.accounts.List(ctx, r.UserID, true)
	if err != nil {
		return TransactionExport{}, fmt.Errorf("list accounts: %w", err)
	}
	categories, err := s.categories.List(ctx, r.UserID, "")
	if err != nil {
		return TransactionExport{}, fmt.Errorf("list categories: %w", err)
	}
	out := TransactionExport{
		Accounts:   make(map[string]domain.Account, len(accounts)),
		Categories: make(map[string]domain.Category, len(categories)),
		Rows:       s.rows(ctx, r.UserID, q.Filter),
	}
	for _, a := range accounts {
		out.Accounts[a.ID] = a
	}
	for _, c := range categories {
		out.Categories[c.ID] = c
	}
	s.auditor.Record(ctx, audit.Entry{
		Event: audit.EventTransactionsExported, UserID: r.UserID, SessionID: r.SessionID,
		IP: r.IP, UserAgent: r.UserAgent, Reason: FormatCSV,
	})
	s.metrics.Exported(datasetTransactions)
	return out, nil
}

// rows pages through the transactions of userID matching f with the list's
// cursor, which bounds the date, so each page reads its own entries.
func (s *Service) rows(ctx context.Context, userID string, f domain.TransactionFilter) iter.Seq2[domain.Transaction, error] {
	return func(yield func(domain.Transaction, error) bool) {
		var after *domain.TransactionCursor
		for {
			page, err := s.ledger.List(ctx, userID, f, after, ExportPageSize)
			if err != nil {
				yield(domain.Transaction{}, fmt.Errorf("list transactions: %w", err))
				return
			}
			for _, t := range page {
				if !yield(t, nil) {
					return
				}
			}
			if len(page) < ExportPageSize {
				return
			}
			last := page[len(page)-1]
			after = &domain.TransactionCursor{Date: last.Date, ID: last.ID}
		}
	}
}
