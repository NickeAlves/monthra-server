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
	store Store
	now   func() time.Time
	max   int
}

// Options tunes the Service.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
	// MaxAccounts defaults to DefaultMaxAccounts.
	MaxAccounts int
}

// NewService returns the service.
func NewService(store Store, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxAccounts <= 0 {
		o.MaxAccounts = DefaultMaxAccounts
	}
	return &Service{store: store, now: o.Now, max: o.MaxAccounts}
}

// clock is the current time at the precision MongoDB keeps (milliseconds),
// so what a write returns equals what later reads return.
func (s *Service) clock() time.Time { return s.now().UTC().Truncate(time.Millisecond) }

// List returns the caller's accounts, oldest first; archived ones only when
// includeArchived.
func (s *Service) List(ctx context.Context, userID string, includeArchived bool) ([]domain.Account, error) {
	list, err := s.store.List(ctx, userID, includeArchived)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	return list, nil
}

// Create validates and stores a new account. It answers
// domain.ErrLimitReached once the caller holds MaxAccounts; two creates at
// the same instant may both pass the count, which still bounds abuse.
func (s *Service) Create(ctx context.Context, userID string, in domain.AccountInput) (domain.Account, error) {
	a, err := domain.NewAccount(userID, in, s.clock())
	if err != nil {
		return domain.Account{}, err
	}
	n, err := s.store.Count(ctx, userID)
	if err != nil {
		return domain.Account{}, fmt.Errorf("count accounts: %w", err)
	}
	if n >= s.max {
		return domain.Account{}, domain.ErrLimitReached
	}
	created, err := s.store.Insert(ctx, a)
	if err != nil {
		return domain.Account{}, fmt.Errorf("create account: %w", err)
	}
	return created, nil
}

// Get returns one of the caller's accounts, or domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, userID, id string) (domain.Account, error) {
	a, err := s.store.ByID(ctx, userID, id)
	if err != nil {
		return domain.Account{}, fmt.Errorf("find account: %w", err)
	}
	return a, nil
}

// Update applies a patch to one of the caller's accounts and returns it. A
// patch that changes nothing writes nothing.
func (s *Service) Update(ctx context.Context, userID, id string, p domain.AccountPatch) (domain.Account, error) {
	current, err := s.Get(ctx, userID, id)
	if err != nil {
		return domain.Account{}, err
	}
	next, changed, err := p.Apply(current, s.clock())
	if err != nil {
		return domain.Account{}, err
	}
	if !changed {
		return current, nil
	}
	if err := s.store.Update(ctx, next); err != nil {
		return domain.Account{}, fmt.Errorf("update account: %w", err)
	}
	return next, nil
}

// Delete removes one of the caller's accounts, or answers domain.ErrNotFound.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	if err := s.store.Delete(ctx, userID, id); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	return nil
}
