package accounts

import (
	"context"
	"fmt"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// DefaultMaxAccounts is how many accounts (archived ones included) one user
// may hold: plenty for a household, and a bound against abuse.
const DefaultMaxAccounts = 100

// Service serves the caller's accounts.
type Service struct {
	store  Store
	ledger Ledger
	now    func() time.Time
	max    int
}

// Options tunes the Service.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
	// MaxAccounts defaults to DefaultMaxAccounts.
	MaxAccounts int
}

// NewService returns the service.
func NewService(store Store, ledger Ledger, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxAccounts <= 0 {
		o.MaxAccounts = DefaultMaxAccounts
	}
	return &Service{store: store, ledger: ledger, now: o.Now, max: o.MaxAccounts}
}

// clock is the current time at the precision MongoDB keeps (milliseconds),
// so what a write returns equals what later reads return.
func (s *Service) clock() time.Time { return s.now().UTC().Truncate(time.Millisecond) }

// today is the day balances count up to when the reader gives none: the
// current day in UTC.
func (s *Service) today(asOf domain.Date) domain.Date {
	if asOf.IsZero() {
		return domain.DateOf(s.now().UTC())
	}
	return asOf
}

// List returns the caller's accounts, oldest first, with their balances up
// to asOf (the reader's today; zero is today in UTC); archived ones only
// when includeArchived.
func (s *Service) List(ctx context.Context, userID string, includeArchived bool, asOf domain.Date) ([]domain.AccountWithBalance, error) {
	list, err := s.store.List(ctx, userID, includeArchived)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	out := make([]domain.AccountWithBalance, len(list))
	if len(list) == 0 {
		return out, nil
	}
	sums, err := s.ledger.Balances(ctx, userID, nil, s.today(asOf))
	if err != nil {
		return nil, fmt.Errorf("sum balances: %w", err)
	}
	for i, a := range list {
		out[i] = a.WithBalance(sums[a.ID])
	}
	return out, nil
}

// withBalance adds the balance of one account up to asOf.
func (s *Service) withBalance(ctx context.Context, a domain.Account, asOf domain.Date) (domain.AccountWithBalance, error) {
	sums, err := s.ledger.Balances(ctx, a.UserID, []string{a.ID}, s.today(asOf))
	if err != nil {
		return domain.AccountWithBalance{}, fmt.Errorf("sum balance: %w", err)
	}
	return a.WithBalance(sums[a.ID]), nil
}

// Create validates and stores a new account, whose balance is its initial
// balance. It answers domain.ErrLimitReached once the caller holds
// MaxAccounts; two creates at the same instant may both pass the count,
// which still bounds abuse.
func (s *Service) Create(ctx context.Context, userID string, in domain.AccountInput) (domain.AccountWithBalance, error) {
	a, err := domain.NewAccount(userID, in, s.clock())
	if err != nil {
		return domain.AccountWithBalance{}, err
	}
	n, err := s.store.Count(ctx, userID)
	if err != nil {
		return domain.AccountWithBalance{}, fmt.Errorf("count accounts: %w", err)
	}
	if n >= s.max {
		return domain.AccountWithBalance{}, domain.ErrLimitReached
	}
	created, err := s.store.Insert(ctx, a)
	if err != nil {
		return domain.AccountWithBalance{}, fmt.Errorf("create account: %w", err)
	}
	return created.WithBalance(domain.Balance{}), nil
}

// Get returns one of the caller's accounts with its balance up to asOf, or
// domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, userID, id string, asOf domain.Date) (domain.AccountWithBalance, error) {
	a, err := s.find(ctx, userID, id)
	if err != nil {
		return domain.AccountWithBalance{}, err
	}
	return s.withBalance(ctx, a, asOf)
}

func (s *Service) find(ctx context.Context, userID, id string) (domain.Account, error) {
	a, err := s.store.ByID(ctx, userID, id)
	if err != nil {
		return domain.Account{}, fmt.Errorf("find account: %w", err)
	}
	return a, nil
}

// Update applies a patch to one of the caller's accounts and returns it
// with its balance up to asOf. A patch that changes nothing writes nothing.
func (s *Service) Update(ctx context.Context, userID, id string, p domain.AccountPatch, asOf domain.Date) (domain.AccountWithBalance, error) {
	current, err := s.find(ctx, userID, id)
	if err != nil {
		return domain.AccountWithBalance{}, err
	}
	next, changed, err := p.Apply(current, s.clock())
	if err != nil {
		return domain.AccountWithBalance{}, err
	}
	if changed {
		if err := s.store.Update(ctx, next); err != nil {
			return domain.AccountWithBalance{}, fmt.Errorf("update account: %w", err)
		}
	}
	return s.withBalance(ctx, next, asOf)
}

// Delete removes one of the caller's accounts, or answers
// domain.ErrNotFound. An account with transactions (as their account or as
// the destination of a transfer) answers domain.ErrAccountInUse: archiving
// keeps its history instead. A transaction created while the account is
// deleted may be left naming it; that is one user racing themselves.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	inUse, err := s.ledger.AccountInUse(ctx, userID, id)
	if err != nil {
		return fmt.Errorf("check account use: %w", err)
	}
	if inUse {
		return domain.ErrAccountInUse
	}
	if err := s.store.Delete(ctx, userID, id); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	return nil
}
