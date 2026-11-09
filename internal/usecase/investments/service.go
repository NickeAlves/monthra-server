package investments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Service manages the caller's portfolio.
type Service struct {
	store      Store
	movements  Movements
	accounts   Accounts
	categories Categories
	afterWrite WriteHook
	metrics    Metrics
	now        func() time.Time
}

// Options tunes the Service.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
	// AfterWrite is told of every write that changes a number of the
	// dashboard (an income written or removed); it defaults to none.
	AfterWrite WriteHook
	// Metrics defaults to counting nothing.
	Metrics Metrics
}

type noHook struct{}

func (noHook) AfterWrite(context.Context, string) {}

type noMetrics struct{}

func (noMetrics) MovementCreated(string) {}

// NewService returns the service.
func NewService(store Store, movements Movements, accounts Accounts, categories Categories, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.AfterWrite == nil {
		o.AfterWrite = noHook{}
	}
	if o.Metrics == nil {
		o.Metrics = noMetrics{}
	}
	return &Service{
		store: store, movements: movements, accounts: accounts, categories: categories,
		afterWrite: o.AfterWrite, metrics: o.Metrics, now: o.Now,
	}
}

// clock is now in UTC at millisecond precision, what MongoDB stores.
func (s *Service) clock() time.Time { return s.now().UTC().Truncate(time.Millisecond) }

// List returns the caller's investments with their positions, oldest first,
// and the totals by currency. Each position replays the movements of its
// investment (a portfolio holds at most domain.MaxInvestments of them, each
// at most domain.MaxMovementsPerInvestment movements); nothing is cached.
func (s *Service) List(ctx context.Context, userID string) (domain.Portfolio, error) {
	invs, err := s.store.List(ctx, userID)
	if err != nil {
		return domain.Portfolio{}, fmt.Errorf("list investments: %w", err)
	}
	all, err := s.movements.All(ctx, userID)
	if err != nil {
		return domain.Portfolio{}, fmt.Errorf("list movements: %w", err)
	}
	out := domain.Portfolio{Investments: make([]domain.InvestmentPosition, len(invs))}
	for i, inv := range invs {
		out.Investments[i] = withPosition(inv, all[inv.ID])
	}
	out.Totals = domain.TotalPortfolio(out.Investments)
	return out, nil
}

// withPosition returns inv with the position its movements make. A read
// never fails on records a race let through (a sale past the units held):
// the position is the one BuildPosition makes of them anyway.
func withPosition(inv domain.Investment, movements []domain.Movement) domain.InvestmentPosition {
	pos, _ := domain.BuildPosition(inv.Mode, movements) //nolint:errcheck // see above
	return domain.InvestmentPosition{Investment: inv, Position: pos}
}

// Get returns investment id of the caller with its position, or
// domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, userID, id string) (domain.InvestmentPosition, error) {
	inv, err := s.store.ByID(ctx, userID, id)
	if err != nil {
		return domain.InvestmentPosition{}, err
	}
	return s.positioned(ctx, inv)
}

// positioned returns inv with its position.
func (s *Service) positioned(ctx context.Context, inv domain.Investment) (domain.InvestmentPosition, error) {
	movements, err := s.movements.Of(ctx, inv.UserID, inv.ID)
	if err != nil {
		return domain.InvestmentPosition{}, fmt.Errorf("list movements: %w", err)
	}
	return withPosition(inv, movements), nil
}

// Create validates and stores a new investment of the caller and returns it,
// with an empty position. The account it names is looked up among the
// caller's accounts only; past domain.MaxInvestments it answers
// domain.ErrLimitReached.
func (s *Service) Create(ctx context.Context, userID string, in domain.InvestmentInput) (domain.InvestmentPosition, error) {
	n, err := s.store.Count(ctx, userID)
	if err != nil {
		return domain.InvestmentPosition{}, fmt.Errorf("count investments: %w", err)
	}
	if n >= domain.MaxInvestments {
		return domain.InvestmentPosition{}, domain.ErrLimitReached
	}
	var refs domain.InvestmentRefs
	if refs.Account, err = s.account(ctx, userID, in.AccountID); err != nil {
		return domain.InvestmentPosition{}, err
	}
	inv, err := domain.NewInvestment(userID, in, refs, s.clock())
	if err != nil {
		return domain.InvestmentPosition{}, err
	}
	saved, err := s.store.Insert(ctx, inv)
	if err != nil {
		return domain.InvestmentPosition{}, fmt.Errorf("store investment: %w", err)
	}
	return withPosition(saved, nil), nil
}

// Update applies p to investment id of the caller and returns it with its
// position. A patch that changes nothing writes nothing; only an account
// the patch moves to is looked up.
func (s *Service) Update(ctx context.Context, userID, id string, p domain.InvestmentPatch) (domain.InvestmentPosition, error) {
	current, err := s.store.ByID(ctx, userID, id)
	if err != nil {
		return domain.InvestmentPosition{}, err
	}
	var refs domain.InvestmentRefs
	if p.AccountID != nil && *p.AccountID != current.AccountID {
		if refs.Account, err = s.account(ctx, userID, *p.AccountID); err != nil {
			return domain.InvestmentPosition{}, err
		}
	}
	next, changed, err := p.Apply(current, refs, s.clock())
	if err != nil {
		return domain.InvestmentPosition{}, err
	}
	if changed {
		if err := s.store.Update(ctx, next); err != nil {
			return domain.InvestmentPosition{}, fmt.Errorf("update investment: %w", err)
		}
	}
	return s.positioned(ctx, next)
}

// Delete removes investment id of the caller with its movements: those
// first, then the investment, then again those a concurrent write added in
// between, so a deletion interrupted halfway is completed by a retry and
// leaves no income counting on the dashboard.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	if _, err := s.store.ByID(ctx, userID, id); err != nil {
		return err
	}
	// Its income leaves the dashboard.
	defer s.afterWrite.AfterWrite(ctx, userID)
	if err := s.movements.DeleteOf(ctx, userID, id); err != nil {
		return fmt.Errorf("delete movements: %w", err)
	}
	if err := s.store.Delete(ctx, userID, id); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("delete investment: %w", err)
	}
	if err := s.movements.DeleteOf(ctx, userID, id); err != nil {
		return fmt.Errorf("delete late movements: %w", err)
	}
	return nil
}

// account finds account id of the caller, nil when there is none (the
// domain reports it as invalid).
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
