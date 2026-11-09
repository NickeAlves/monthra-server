package transactions

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Page sizes of List.
const (
	DefaultPageSize = 50
	MaxPageSize     = 100
)

// Query is one page of a listing: the filter, where the previous page
// ended (nil for the first one) and the page size.
type Query struct {
	Filter domain.TransactionFilter
	After  *domain.TransactionCursor
	// Limit is between 1 and MaxPageSize; 0 means DefaultPageSize.
	Limit int
	// AsOf is the reader's today, up to which the occurrences of the
	// recurrences are created first; zero is today in UTC.
	AsOf domain.Date
}

// Page is a page of transactions and where the next one starts, or nil on
// the last page.
type Page struct {
	Items []domain.Transaction
	Next  *domain.TransactionCursor
}

// Service serves the caller's transactions.
type Service struct {
	store      Store
	accounts   Accounts
	categories Categories
	metrics    Metrics
	series     Series
	afterWrite WriteHook
	now        func() time.Time
}

// Options tunes the Service.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
	// Metrics defaults to counting nothing.
	Metrics Metrics
	// Series defaults to no recurrences.
	Series Series
	// AfterWrite is told of every write; it defaults to none.
	AfterWrite WriteHook
}

type noHook struct{}

func (noHook) AfterWrite(context.Context, string) {}

type noMetrics struct{}

func (noMetrics) TransactionCreated(string) {}

type noSeries struct{}

func (noSeries) Materialize(context.Context, string, domain.Date)                     {}
func (noSeries) Follow(context.Context, domain.Transaction, domain.Transaction) error { return nil }
func (noSeries) EndBefore(context.Context, domain.Transaction) error                  { return nil }
func (noSeries) Remove(context.Context, domain.Transaction) error                     { return nil }

// NewService returns the service.
func NewService(store Store, accounts Accounts, categories Categories, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Metrics == nil {
		o.Metrics = noMetrics{}
	}
	if o.Series == nil {
		o.Series = noSeries{}
	}
	if o.AfterWrite == nil {
		o.AfterWrite = noHook{}
	}
	return &Service{
		store: store, accounts: accounts, categories: categories, metrics: o.Metrics, series: o.Series,
		afterWrite: o.AfterWrite, now: o.Now,
	}
}

// clock is the current time at the precision MongoDB keeps (milliseconds),
// so what a write returns equals what later reads return.
func (s *Service) clock() time.Time { return s.now().UTC().Truncate(time.Millisecond) }

// List returns one page of the caller's transactions, newest first, after
// creating the occurrences of the caller's recurrences due up to q.AsOf; a
// page size out of bounds is a *domain.ValidationError.
func (s *Service) List(ctx context.Context, userID string, q Query) (Page, error) {
	limit := q.Limit
	switch {
	case limit == 0:
		limit = DefaultPageSize
	case limit < 0 || limit > MaxPageSize:
		return Page{}, &domain.ValidationError{Fields: []domain.FieldError{{Field: "limit", Code: domain.CodeOutOfRange}}}
	}
	s.series.Materialize(ctx, userID, q.AsOf)
	// One more than the page tells whether another page follows.
	items, err := s.store.List(ctx, userID, q.Filter, q.After, limit+1)
	if err != nil {
		return Page{}, fmt.Errorf("list transactions: %w", err)
	}
	page := Page{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.Next = &domain.TransactionCursor{Date: last.Date, ID: last.ID}
	}
	return page, nil
}

// Create validates and stores a new transaction, or the installments of a
// purchase (in.Installments), and returns it or the first installment. The
// accounts and the category it names are looked up among the caller's
// records only.
func (s *Service) Create(ctx context.Context, userID string, in domain.TransactionInput) (domain.Transaction, error) {
	kind := domain.TransactionKind(in.Kind)
	var refs domain.TransactionRefs
	var err error
	if refs.Account, err = s.account(ctx, userID, in.AccountID); err != nil {
		return domain.Transaction{}, err
	}
	if kind == domain.TransactionTransfer {
		if refs.ToAccount, err = s.account(ctx, userID, in.ToAccountID); err != nil {
			return domain.Transaction{}, err
		}
	} else if refs.Category, err = s.category(ctx, userID, in.CategoryID); err != nil {
		return domain.Transaction{}, err
	}
	ts, err := domain.NewTransactions(userID, in, refs, s.clock())
	if err != nil {
		return domain.Transaction{}, err
	}
	// After the attempt, even a failed one: a partial write may be stored.
	defer s.afterWrite.AfterWrite(ctx, userID)
	var created []domain.Transaction
	if len(ts) == 1 {
		var t domain.Transaction
		t, err = s.store.Insert(ctx, ts[0])
		created = []domain.Transaction{t}
	} else {
		created, err = s.store.InsertInstallments(ctx, ts)
	}
	if err != nil {
		return domain.Transaction{}, fmt.Errorf("create transaction: %w", err)
	}
	for _, t := range created {
		s.metrics.TransactionCreated(string(t.Kind))
	}
	return created[0], nil
}

// Get returns one of the caller's transactions, or domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, userID, id string) (domain.Transaction, error) {
	t, err := s.store.ByID(ctx, userID, id)
	if err != nil {
		return domain.Transaction{}, fmt.Errorf("find transaction: %w", err)
	}
	return t, nil
}

// Update applies a patch to one of the caller's transactions and returns
// it. On an installment, scope reaches the next installments of its
// purchase (ScopeFuture) or all of them (ScopeAll); on an occurrence, the
// next occurrences of its recurrence or all of them, and the recurrence
// itself, so the occurrences still to come take the changes too. They take
// the changes the patch made to this one (domain.TransactionPatch.ApplyInScope). Only
// the records the patch moves it to are looked up, and the card a leg stays
// on when the patch moves its day or names its invoice (for the card's
// settings); a patch that changes nothing writes nothing.
func (s *Service) Update(ctx context.Context, userID, id string, p domain.TransactionPatch, scope domain.EditScope) (domain.Transaction, error) {
	current, err := s.Get(ctx, userID, id)
	if err != nil {
		return domain.Transaction{}, err
	}
	redated := p.Date != nil && *p.Date != current.Date.String()
	var refs domain.TransactionRefs
	switch {
	case moved(p.AccountID, current.AccountID):
		refs.Account, err = s.account(ctx, userID, *p.AccountID)
	case needsCard(redated, p.InvoiceMonth, current.InvoiceMonth):
		refs.Account, err = s.account(ctx, userID, current.AccountID)
	}
	if err != nil {
		return domain.Transaction{}, err
	}
	if current.IsTransfer() {
		switch {
		case moved(p.ToAccountID, current.ToAccountID):
			refs.ToAccount, err = s.account(ctx, userID, *p.ToAccountID)
		case needsCard(redated, p.ToInvoiceMonth, current.ToInvoiceMonth):
			refs.ToAccount, err = s.account(ctx, userID, current.ToAccountID)
		}
		if err != nil {
			return domain.Transaction{}, err
		}
	} else if moved(p.CategoryID, current.CategoryID) {
		if refs.Category, err = s.category(ctx, userID, *p.CategoryID); err != nil {
			return domain.Transaction{}, err
		}
	}
	others, err := s.inScope(ctx, current, scope)
	if err != nil {
		return domain.Transaction{}, err
	}
	edit, err := p.ApplyInScope(current, scope, others, refs, s.clock())
	if err != nil {
		return domain.Transaction{}, err
	}
	if len(edit.Others) > 0 || edit.Changed {
		defer s.afterWrite.AfterWrite(ctx, userID)
	}
	// The others first, then the recurrence, and this one last: until this
	// one is stored, sending the same patch again finds the same changes on
	// it and completes them.
	if len(edit.Others) > 0 {
		if err := s.store.UpdateMany(ctx, edit.Others); err != nil {
			return domain.Transaction{}, fmt.Errorf("update series: %w", err)
		}
	}
	if !edit.Changed {
		return current, nil
	}
	if current.Recurrence != nil && scope != domain.ScopeThis {
		if err := s.series.Follow(ctx, current, edit.Next); err != nil {
			return domain.Transaction{}, fmt.Errorf("follow the recurrence: %w", err)
		}
	}
	if err := s.store.Update(ctx, edit.Next); err != nil {
		return domain.Transaction{}, fmt.Errorf("update transaction: %w", err)
	}
	return edit.Next, nil
}

// Delete removes one of the caller's transactions or, on an installment,
// the next installments of its purchase too (ScopeFuture) or all of them
// (ScopeAll); the others keep their numbers. On an occurrence, ScopeFuture
// ends its recurrence before it and removes it and the next occurrences;
// ScopeAll removes the recurrence and every occurrence. The recurrence goes
// first, so no read creates occurrences again meanwhile. It answers
// domain.ErrNotFound for a transaction the caller does not hold.
func (s *Service) Delete(ctx context.Context, userID, id string, scope domain.EditScope) error {
	defer s.afterWrite.AfterWrite(ctx, userID)
	if scope != domain.ScopeThis {
		current, err := s.Get(ctx, userID, id)
		if err != nil {
			return err
		}
		switch {
		case current.Installment != nil:
			from := 1
			if scope == domain.ScopeFuture {
				from = current.Installment.Number
			}
			if err := s.store.DeleteInstallments(ctx, userID, current.Installment.GroupID, from); err != nil {
				return fmt.Errorf("delete installments: %w", err)
			}
			return nil
		case current.Recurrence != nil:
			var from domain.Date
			if scope == domain.ScopeFuture {
				from = current.Recurrence.Date
				err = s.series.EndBefore(ctx, current)
			} else {
				err = s.series.Remove(ctx, current)
			}
			if err != nil {
				return fmt.Errorf("end the recurrence: %w", err)
			}
			if err := s.store.DeleteOccurrences(ctx, userID, current.Recurrence.ID, from); err != nil {
				return fmt.Errorf("delete occurrences: %w", err)
			}
			return nil
		}
	}
	if err := s.store.Delete(ctx, userID, id); err != nil {
		return fmt.Errorf("delete transaction: %w", err)
	}
	return nil
}

// inScope returns the other transactions of t's series that scope reaches:
// the installments of its purchase numbered from t's (ScopeFuture) or all
// of them (ScopeAll), or the occurrences of its recurrence from t's rule
// day or all of them; none for ScopeThis or outside a series.
func (s *Service) inScope(ctx context.Context, t domain.Transaction, scope domain.EditScope) ([]domain.Transaction, error) {
	if scope == domain.ScopeThis {
		return nil, nil
	}
	switch {
	case t.Installment != nil:
		from := 1
		if scope == domain.ScopeFuture {
			from = t.Installment.Number
		}
		list, err := s.store.Installments(ctx, t.UserID, t.Installment.GroupID, from)
		if err != nil {
			return nil, fmt.Errorf("find installments: %w", err)
		}
		return list, nil
	case t.Recurrence != nil:
		var from domain.Date
		if scope == domain.ScopeFuture {
			from = t.Recurrence.Date
		}
		list, err := s.store.Occurrences(ctx, t.UserID, t.Recurrence.ID, from)
		if err != nil {
			return nil, fmt.Errorf("find occurrences: %w", err)
		}
		return list, nil
	default:
		return nil, nil
	}
}

// needsCard reports whether a leg that stays on its account needs that
// account looked up: when the patch names the leg's invoice, or moves the
// day of a leg on a card (only those have an invoice).
func needsCard(redated bool, invoice *string, current domain.Month) bool {
	return invoice != nil || (redated && !current.IsZero())
}

// moved reports whether a patch names a reference other than the current one.
func moved(patch *string, current string) bool {
	return patch != nil && *patch != "" && *patch != current
}

// account finds account id of the caller: nil when the id is empty or names
// none of the caller's accounts, which the domain reports per field.
func (s *Service) account(ctx context.Context, userID, id string) (*domain.Account, error) {
	if id == "" {
		return nil, nil
	}
	a, err := s.accounts.ByID(ctx, userID, id)
	switch {
	case err == nil:
		return &a, nil
	case errors.Is(err, domain.ErrNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("find account: %w", err)
	}
}

// category finds category id of the caller, like account.
func (s *Service) category(ctx context.Context, userID, id string) (*domain.Category, error) {
	if id == "" {
		return nil, nil
	}
	c, err := s.categories.ByID(ctx, userID, id)
	switch {
	case err == nil:
		return &c, nil
	case errors.Is(err, domain.ErrNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("find category: %w", err)
	}
}
