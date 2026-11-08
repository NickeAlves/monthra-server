package recurrences

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

const (
	// DefaultMaxRecurrences is how many recurrences (ended ones included)
	// one user may hold.
	DefaultMaxRecurrences = 200
	// MaxOccurrencesPerPass bounds the occurrences one read creates, so a
	// long history (a start years ago, a long absence) costs a few reads,
	// not one slow one.
	MaxOccurrencesPerPass = 500
	// MaxUpcoming bounds the occurrences one projection returns.
	MaxUpcoming = 1000
	// DefaultUpcomingDays and MaxUpcomingDays are how far past today a
	// projection reaches by default and at most.
	DefaultUpcomingDays = 30
	MaxUpcomingDays     = 366
)

// Service serves the caller's recurrences and creates their occurrences.
type Service struct {
	store      Store
	ledger     Ledger
	accounts   Accounts
	categories Categories
	metrics    Metrics
	logger     *slog.Logger
	now        func() time.Time
	max        int
}

// Options tunes the Service.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
	// Logger records the failures of Materialize; it defaults to none.
	Logger *slog.Logger
	// Metrics defaults to counting nothing.
	Metrics Metrics
	// MaxRecurrences defaults to DefaultMaxRecurrences.
	MaxRecurrences int
}

type noMetrics struct{}

func (noMetrics) TransactionCreated(string) {}

// NewService returns the service.
func NewService(store Store, ledger Ledger, accounts Accounts, categories Categories, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Metrics == nil {
		o.Metrics = noMetrics{}
	}
	if o.MaxRecurrences <= 0 {
		o.MaxRecurrences = DefaultMaxRecurrences
	}
	return &Service{
		store: store, ledger: ledger, accounts: accounts, categories: categories,
		metrics: o.Metrics, logger: o.Logger, now: o.Now, max: o.MaxRecurrences,
	}
}

// clock is the current time at the precision MongoDB keeps (milliseconds),
// so what a write returns equals what later reads return.
func (s *Service) clock() time.Time { return s.now().UTC().Truncate(time.Millisecond) }

// horizon is the last day whose occurrences are created: asOf, the
// reader's local today (zero is today in UTC), within a day of today in
// UTC, which every time zone's today is. So no reader creates the future.
func (s *Service) horizon(asOf domain.Date) domain.Date { return domain.Today(asOf, s.now()) }

// Materialize creates the occurrences of the caller's recurrences due up
// to asOf (see horizon), at most MaxOccurrencesPerPass; the rest come with
// the next read. It runs before every read of the transactions, so it
// never fails one: a failure is logged and the read goes on with what was
// stored.
func (s *Service) Materialize(ctx context.Context, userID string, asOf domain.Date) {
	if err := s.materialize(ctx, userID, s.horizon(asOf)); err != nil {
		s.logger.WarnContext(ctx, "materialize recurrences failed", "error", err)
	}
}

// materialize creates the occurrences due through day through. Each
// recurrence stores its occurrences first and advances after, with a
// compare-and-set: a pass interrupted, or two readers at once, store the
// same occurrences again, which the store skips, and advance once.
func (s *Service) materialize(ctx context.Context, userID string, through domain.Date) error {
	due, err := s.store.Due(ctx, userID, through, s.max)
	if err != nil {
		return fmt.Errorf("find due recurrences: %w", err)
	}
	if len(due) == 0 {
		return nil
	}
	accounts, err := s.accountsOf(ctx, userID)
	if err != nil {
		return err
	}
	now := s.clock()
	budget := MaxOccurrencesPerPass
	for _, r := range due {
		refs := accounts.of(r)
		if refs.Account == nil || (r.Kind == domain.TransactionTransfer && refs.ToAccount == nil) {
			// Only a deletion racing the recurrence leaves it on an account
			// that is gone; it stays due.
			s.logger.WarnContext(ctx, "recurrence on a missing account", "recurrence_id", r.ID)
			continue
		}
		ts, next := r.Occurrences(refs, through, budget, now)
		if len(ts) == 0 {
			continue
		}
		created, err := s.ledger.InsertOccurrences(ctx, ts)
		if err != nil {
			return fmt.Errorf("create occurrences: %w", err)
		}
		for range created {
			s.metrics.TransactionCreated(string(r.Kind))
		}
		if err := s.store.Advance(ctx, userID, r.ID, r.Next, next, ts[len(ts)-1].Recurrence.Date); err != nil {
			return fmt.Errorf("advance recurrence: %w", err)
		}
		if budget -= len(ts); budget <= 0 {
			break
		}
	}
	return nil
}

// accountSet is the caller's accounts by id.
type accountSet map[string]*domain.Account

func (s *Service) accountsOf(ctx context.Context, userID string) (accountSet, error) {
	list, err := s.accounts.List(ctx, userID, true)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	set := make(accountSet, len(list))
	for i := range list {
		set[list[i].ID] = &list[i]
	}
	return set, nil
}

// of returns the accounts recurrence r names.
func (a accountSet) of(r domain.Recurrence) domain.TransactionRefs {
	refs := domain.TransactionRefs{Account: a[r.AccountID]}
	if r.Kind == domain.TransactionTransfer {
		refs.ToAccount = a[r.ToAccountID]
	}
	return refs
}

// List returns the caller's recurrences, oldest first, after creating the
// occurrences due up to asOf.
func (s *Service) List(ctx context.Context, userID string, asOf domain.Date) ([]domain.Recurrence, error) {
	s.Materialize(ctx, userID, asOf)
	list, err := s.store.List(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list recurrences: %w", err)
	}
	return list, nil
}

// Create validates and stores a new recurrence, creates its occurrences due
// up to asOf (a start in the past creates its history), and returns it. The
// records it names are looked up among the caller's only. It answers
// domain.ErrLimitReached once the caller holds MaxRecurrences.
func (s *Service) Create(ctx context.Context, userID string, in domain.RecurrenceInput, asOf domain.Date) (domain.Recurrence, error) {
	var refs domain.TransactionRefs
	var err error
	if refs.Account, err = s.account(ctx, userID, in.AccountID); err != nil {
		return domain.Recurrence{}, err
	}
	if domain.TransactionKind(in.Kind) == domain.TransactionTransfer {
		if refs.ToAccount, err = s.account(ctx, userID, in.ToAccountID); err != nil {
			return domain.Recurrence{}, err
		}
	} else if refs.Category, err = s.category(ctx, userID, in.CategoryID); err != nil {
		return domain.Recurrence{}, err
	}
	r, err := domain.NewRecurrence(userID, in, refs, s.clock())
	if err != nil {
		return domain.Recurrence{}, err
	}
	n, err := s.store.Count(ctx, userID)
	if err != nil {
		return domain.Recurrence{}, fmt.Errorf("count recurrences: %w", err)
	}
	if n >= s.max {
		return domain.Recurrence{}, domain.ErrLimitReached
	}
	created, err := s.store.Insert(ctx, r)
	if err != nil {
		return domain.Recurrence{}, fmt.Errorf("create recurrence: %w", err)
	}
	return s.fresh(ctx, created, asOf), nil
}

// fresh creates the occurrences due up to asOf and returns r as it stands
// after them, or as it was when it cannot be read again.
func (s *Service) fresh(ctx context.Context, r domain.Recurrence, asOf domain.Date) domain.Recurrence {
	s.Materialize(ctx, r.UserID, asOf)
	if after, err := s.store.ByID(ctx, r.UserID, r.ID); err == nil {
		return after
	}
	return r
}

// Get returns one of the caller's recurrences after creating the
// occurrences due up to asOf, or domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, userID, id string, asOf domain.Date) (domain.Recurrence, error) {
	s.Materialize(ctx, userID, asOf)
	return s.find(ctx, userID, id)
}

func (s *Service) find(ctx context.Context, userID, id string) (domain.Recurrence, error) {
	r, err := s.store.ByID(ctx, userID, id)
	if err != nil {
		return domain.Recurrence{}, fmt.Errorf("find recurrence: %w", err)
	}
	return r, nil
}

// Update applies a patch to one of the caller's recurrences: what its next
// occurrences will be, never the transactions already created. It first
// creates the occurrences due up to asOf, so the patch sees the last one,
// and after it those the new rule makes due. Only the records the patch
// moves it to are looked up, and its accounts when the patch may bring it
// back to life (it ended).
func (s *Service) Update(ctx context.Context, userID, id string, p domain.RecurrencePatch, asOf domain.Date) (domain.Recurrence, error) {
	current, err := s.Get(ctx, userID, id, asOf)
	if err != nil {
		return domain.Recurrence{}, err
	}
	var refs domain.TransactionRefs
	refs.Account, err = s.ref(ctx, userID, p.AccountID, current.AccountID, !current.Active())
	if err != nil {
		return domain.Recurrence{}, err
	}
	if current.Kind == domain.TransactionTransfer {
		if refs.ToAccount, err = s.ref(ctx, userID, p.ToAccountID, current.ToAccountID, !current.Active()); err != nil {
			return domain.Recurrence{}, err
		}
	} else if moved(p.CategoryID, current.CategoryID) {
		if refs.Category, err = s.category(ctx, userID, *p.CategoryID); err != nil {
			return domain.Recurrence{}, err
		}
	}
	next, changed, err := p.Apply(current, refs, s.clock())
	if err != nil {
		return domain.Recurrence{}, err
	}
	if !changed {
		return current, nil
	}
	if err := s.store.Update(ctx, next); err != nil {
		return domain.Recurrence{}, fmt.Errorf("update recurrence: %w", err)
	}
	return s.fresh(ctx, next, asOf), nil
}

// ref looks up the account a patch moves a recurrence to, or its current
// one when keep, and none otherwise.
func (s *Service) ref(ctx context.Context, userID string, patch *string, current string, keep bool) (*domain.Account, error) {
	switch {
	case moved(patch, current):
		return s.account(ctx, userID, *patch)
	case keep:
		return s.account(ctx, userID, current)
	default:
		return nil, nil
	}
}

// Delete removes one of the caller's recurrences, or answers
// domain.ErrNotFound. Its transactions stay, still linked to it.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	if err := s.store.Delete(ctx, userID, id); err != nil {
		return fmt.Errorf("delete recurrence: %w", err)
	}
	return nil
}

// Upcoming returns the occurrences of the caller's recurrences not created
// yet (after asOf, once the due ones are) up to day to, the earliest first,
// at most MaxUpcoming: a projection, stored nowhere. A zero to is
// DefaultUpcomingDays after asOf; one before asOf, or more than
// MaxUpcomingDays after it, is a *domain.ValidationError.
func (s *Service) Upcoming(ctx context.Context, userID string, to, asOf domain.Date) ([]domain.Transaction, error) {
	day := s.horizon(asOf)
	switch {
	case to.IsZero():
		to = day.AddDays(DefaultUpcomingDays)
	case to.Before(day):
		return nil, &domain.ValidationError{Fields: []domain.FieldError{{Field: "to", Code: domain.CodeInvalid}}}
	case day.AddDays(MaxUpcomingDays).Before(to):
		return nil, &domain.ValidationError{Fields: []domain.FieldError{{Field: "to", Code: domain.CodeOutOfRange}}}
	}
	list, err := s.List(ctx, userID, asOf)
	if err != nil {
		return nil, err
	}
	var out []domain.Transaction
	var accounts accountSet
	for _, r := range list {
		if !r.Active() || to.Before(r.Next) {
			continue
		}
		if accounts == nil {
			if accounts, err = s.accountsOf(ctx, userID); err != nil {
				return nil, err
			}
		}
		ts, _ := r.Occurrences(accounts.of(r), to, MaxUpcoming, s.clock())
		out = append(out, ts...)
	}
	slices.SortStableFunc(out, func(a, b domain.Transaction) int {
		if c := a.Date.Time().Compare(b.Date.Time()); c != 0 {
			return c
		}
		return cmp.Compare(a.Recurrence.ID, b.Recurrence.ID)
	})
	if len(out) > MaxUpcoming {
		out = out[:MaxUpcoming]
	}
	if out == nil {
		out = []domain.Transaction{}
	}
	return out, nil
}

// AccountUsage reports whether a recurrence of the caller names account
// id, and whether one of those is still active.
func (s *Service) AccountUsage(ctx context.Context, userID, id string) (used, active bool, err error) {
	used, active, err = s.store.AccountUsage(ctx, userID, id)
	if err != nil {
		return false, false, fmt.Errorf("check recurrence use: %w", err)
	}
	return used, active, nil
}

// Follow applies to the recurrence of an occurrence the changes a scoped
// edit made to that occurrence (before and after it), so the next
// occurrences take them too. A recurrence deleted since has nothing to
// follow.
func (s *Service) Follow(ctx context.Context, before, after domain.Transaction) error {
	r, ok, err := s.recurrenceOf(ctx, after)
	if err != nil || !ok {
		return err
	}
	next, changed := r.Follow(before, after, s.clock())
	if !changed {
		return nil
	}
	return ignoreGone(s.store.Update(ctx, next), "update recurrence")
}

// EndBefore ends the recurrence of an occurrence on the day before the
// occurrence's rule day, so it creates nothing from there on. When that
// leaves it no day at all, the recurrence goes, unless occurrences from
// before that day remain: then it stays, ended, so they still name it.
func (s *Service) EndBefore(ctx context.Context, occurrence domain.Transaction) error {
	r, ok, err := s.recurrenceOf(ctx, occurrence)
	if err != nil || !ok {
		return err
	}
	day, hasEarlier := occurrence.Recurrence.Date, false
	if !r.Rule.Start.Before(day) {
		if hasEarlier, err = s.ledger.HasOccurrencesBefore(ctx, r.UserID, r.ID, day); err != nil {
			return fmt.Errorf("find earlier occurrences: %w", err)
		}
	}
	next, keep := r.EndBefore(day, hasEarlier, s.clock())
	if !keep {
		return ignoreGone(s.store.Delete(ctx, r.UserID, r.ID), "delete recurrence")
	}
	return ignoreGone(s.store.Update(ctx, next), "end recurrence")
}

// Remove deletes the recurrence of an occurrence; one already gone is no
// error.
func (s *Service) Remove(ctx context.Context, occurrence domain.Transaction) error {
	if occurrence.Recurrence == nil {
		return nil
	}
	return ignoreGone(s.store.Delete(ctx, occurrence.UserID, occurrence.Recurrence.ID), "delete recurrence")
}

// recurrenceOf finds the recurrence of an occurrence, reporting false when
// t is none or its recurrence was deleted.
func (s *Service) recurrenceOf(ctx context.Context, t domain.Transaction) (domain.Recurrence, bool, error) {
	if t.Recurrence == nil {
		return domain.Recurrence{}, false, nil
	}
	r, err := s.store.ByID(ctx, t.UserID, t.Recurrence.ID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return domain.Recurrence{}, false, nil
	case err != nil:
		return domain.Recurrence{}, false, fmt.Errorf("find recurrence: %w", err)
	}
	return r, true, nil
}

// ignoreGone wraps err, except the not found of a recurrence deleted
// meanwhile.
func ignoreGone(err error, what string) error {
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
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
