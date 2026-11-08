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
	now        func() time.Time
}

// Options tunes the Service.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
	// Metrics defaults to counting nothing.
	Metrics Metrics
}

type noMetrics struct{}

func (noMetrics) TransactionCreated(string) {}

// NewService returns the service.
func NewService(store Store, accounts Accounts, categories Categories, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Metrics == nil {
		o.Metrics = noMetrics{}
	}
	return &Service{store: store, accounts: accounts, categories: categories, metrics: o.Metrics, now: o.Now}
}

// clock is the current time at the precision MongoDB keeps (milliseconds),
// so what a write returns equals what later reads return.
func (s *Service) clock() time.Time { return s.now().UTC().Truncate(time.Millisecond) }

// List returns one page of the caller's transactions, newest first; a page
// size out of bounds is a *domain.ValidationError.
func (s *Service) List(ctx context.Context, userID string, q Query) (Page, error) {
	limit := q.Limit
	switch {
	case limit == 0:
		limit = DefaultPageSize
	case limit < 0 || limit > MaxPageSize:
		return Page{}, &domain.ValidationError{Fields: []domain.FieldError{{Field: "limit", Code: domain.CodeOutOfRange}}}
	}
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

// Create validates and stores a new transaction. The accounts and the
// category it names are looked up among the caller's records only.
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
	t, err := domain.NewTransaction(userID, in, refs, s.clock())
	if err != nil {
		return domain.Transaction{}, err
	}
	created, err := s.store.Insert(ctx, t)
	if err != nil {
		return domain.Transaction{}, fmt.Errorf("create transaction: %w", err)
	}
	s.metrics.TransactionCreated(string(created.Kind))
	return created, nil
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
// it. Only the records the patch moves it to are looked up, and the card a
// leg stays on when the patch moves its day or names its invoice (for the
// card's settings); a patch that changes nothing writes nothing.
func (s *Service) Update(ctx context.Context, userID, id string, p domain.TransactionPatch) (domain.Transaction, error) {
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
	next, changed, err := p.Apply(current, refs, s.clock())
	if err != nil {
		return domain.Transaction{}, err
	}
	if !changed {
		return current, nil
	}
	if err := s.store.Update(ctx, next); err != nil {
		return domain.Transaction{}, fmt.Errorf("update transaction: %w", err)
	}
	return next, nil
}

// Delete removes one of the caller's transactions, or answers
// domain.ErrNotFound.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	if err := s.store.Delete(ctx, userID, id); err != nil {
		return fmt.Errorf("delete transaction: %w", err)
	}
	return nil
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
