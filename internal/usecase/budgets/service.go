package budgets

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Service manages the caller's budgets.
type Service struct {
	store       Store
	categories  Categories
	ledger      Ledger
	recurrences Recurrences
	roll        roller
	now         func() time.Time
}

// Options tunes the Service and the Alerts.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// NewService returns the service. It comes after the recurrences, whose
// occurrences it creates before reading what a budget spent.
func NewService(store Store, marker Marker, categories Categories, ledger Ledger, recurrences Recurrences, o Options) *Service {
	o = o.withDefaults()
	return &Service{
		store: store, categories: categories, ledger: ledger, recurrences: recurrences,
		roll: roller{store: store, marker: marker, now: o.Now}, now: o.Now,
	}
}

// prepare readies the caller's data for a reader whose today is asOf (their
// local day, within a day of today in UTC: domain.Today): the occurrences
// due are created and the budgets carried over through the reader's month,
// which it returns.
func (s *Service) prepare(ctx context.Context, userID string, asOf domain.Date) (domain.Month, error) {
	today := domain.Today(asOf, s.now())
	s.recurrences.Materialize(ctx, userID, today)
	current := domain.MonthOf(today)
	if err := s.roll.ensure(ctx, userID, current); err != nil {
		return domain.Month{}, err
	}
	return current, nil
}

// List returns the caller's budgets of month (zero is the reader's month)
// with what each spent and has scheduled, and the totals by currency. A
// month after the reader's is a projection: the budgets of the reader's
// month, with no ids, since the month gets its own when it starts, and what
// is already spent in it (installments).
func (s *Service) List(ctx context.Context, userID string, month domain.Month, asOf domain.Date) (domain.BudgetMonth, error) {
	current, err := s.prepare(ctx, userID, asOf)
	if err != nil {
		return domain.BudgetMonth{}, err
	}
	if month.IsZero() {
		month = current
	}
	projected := current.Before(month)
	source := month
	if projected {
		source = current
	}
	list, err := s.store.List(ctx, userID, source)
	if err != nil {
		return domain.BudgetMonth{}, fmt.Errorf("list budgets: %w", err)
	}
	if projected {
		list = domain.RollBudgets(list, month, clock(s.now))
	}
	usages, err := s.use(ctx, userID, month, list)
	if err != nil {
		return domain.BudgetMonth{}, err
	}
	return domain.BudgetMonth{Month: month, Projected: projected, Budgets: usages, Totals: domain.TotalBudgets(usages)}, nil
}

// use returns the usage of the budgets of month in list.
func (s *Service) use(ctx context.Context, userID string, month domain.Month, list []domain.Budget) ([]domain.BudgetUsage, error) {
	if len(list) == 0 {
		return []domain.BudgetUsage{}, nil
	}
	from, to := month.Day(1), month.LastDay()
	flows, err := s.ledger.Flows(ctx, userID, from, to)
	if err != nil {
		return nil, fmt.Errorf("sum flows: %w", err)
	}
	scheduled, err := s.recurrences.Scheduled(ctx, userID, from, to)
	if err != nil {
		return nil, fmt.Errorf("project recurrences: %w", err)
	}
	return domain.UseBudgets(list, month, flows, scheduled), nil
}

// useOne returns the usage of budget b.
func (s *Service) useOne(ctx context.Context, b domain.Budget) (domain.BudgetUsage, error) {
	usages, err := s.use(ctx, b.UserID, b.Month, []domain.Budget{b})
	if err != nil {
		return domain.BudgetUsage{}, err
	}
	return usages[0], nil
}

// Create validates and stores a new budget for a reader whose today is asOf,
// and returns it with its usage. The category must be an expense category
// of the caller (else a *domain.ValidationError on categoryId), and the
// month the reader's or one gone by; a category with a budget in the month
// answers domain.ErrBudgetExists. The alerts the budget is already past
// are marked as given: the owner sees its status as they create it.
func (s *Service) Create(ctx context.Context, userID string, in domain.BudgetInput, asOf domain.Date) (domain.BudgetUsage, error) {
	b, err := domain.NewBudget(userID, in, domain.MonthOf(domain.Today(asOf, s.now())), clock(s.now))
	if err != nil {
		return domain.BudgetUsage{}, err
	}
	if err := s.checkCategory(ctx, userID, b.CategoryID); err != nil {
		return domain.BudgetUsage{}, err
	}
	// Carried over first, so the month's own budgets are there to clash
	// with.
	if _, err := s.prepare(ctx, userID, asOf); err != nil {
		return domain.BudgetUsage{}, err
	}
	u, err := s.useOne(ctx, b)
	if err != nil {
		return domain.BudgetUsage{}, err
	}
	created, err := s.store.Insert(ctx, b.Rearmed(u.Spent, clock(s.now)))
	if err != nil {
		if errors.Is(err, domain.ErrBudgetExists) {
			return domain.BudgetUsage{}, err
		}
		return domain.BudgetUsage{}, fmt.Errorf("create budget: %w", err)
	}
	u.Budget = created
	return u, nil
}

// checkCategory checks that id names an expense category of the caller.
func (s *Service) checkCategory(ctx context.Context, userID, id string) error {
	refuse := func(code string) error {
		return &domain.ValidationError{Fields: []domain.FieldError{{Field: "categoryId", Code: code}}}
	}
	c, err := s.categories.ByID(ctx, userID, id)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return refuse(domain.CodeInvalid)
	case err != nil:
		return fmt.Errorf("find category: %w", err)
	case c.Kind != domain.CategoryExpense:
		return refuse(domain.CodeKindMismatch)
	}
	return nil
}

// Get returns one of the caller's budgets with its usage, or
// domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, userID, id string, asOf domain.Date) (domain.BudgetUsage, error) {
	if _, err := s.prepare(ctx, userID, asOf); err != nil {
		return domain.BudgetUsage{}, err
	}
	b, err := s.find(ctx, userID, id)
	if err != nil {
		return domain.BudgetUsage{}, err
	}
	return s.useOne(ctx, b)
}

func (s *Service) find(ctx context.Context, userID, id string) (domain.Budget, error) {
	b, err := s.store.ByID(ctx, userID, id)
	if err != nil {
		return domain.Budget{}, fmt.Errorf("find budget: %w", err)
	}
	return b, nil
}

// Update applies a patch to one of the caller's budgets and returns it with
// its usage. The alert marks follow the new limit and share silently: those
// now crossed need no alert, and those no longer crossed go out when
// spending crosses them again. A patch that changes nothing writes nothing.
func (s *Service) Update(ctx context.Context, userID, id string, p domain.BudgetPatch, asOf domain.Date) (domain.BudgetUsage, error) {
	if _, err := s.prepare(ctx, userID, asOf); err != nil {
		return domain.BudgetUsage{}, err
	}
	current, err := s.find(ctx, userID, id)
	if err != nil {
		return domain.BudgetUsage{}, err
	}
	now := clock(s.now)
	next, changed, err := p.Apply(current, now)
	if err != nil {
		return domain.BudgetUsage{}, err
	}
	u, err := s.useOne(ctx, next)
	if err != nil || !changed {
		return u, err
	}
	next = next.Rearmed(u.Spent, now)
	if err := s.store.Update(ctx, next); err != nil {
		return domain.BudgetUsage{}, fmt.Errorf("update budget: %w", err)
	}
	u.Budget = next
	return u, nil
}

// Delete removes one of the caller's budgets, or answers
// domain.ErrNotFound. A budget of the reader's month is not carried over to
// the next one, which gets the month's budgets when it starts.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	if err := s.store.Delete(ctx, userID, id); err != nil {
		return fmt.Errorf("delete budget: %w", err)
	}
	return nil
}
