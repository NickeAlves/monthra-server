package categories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// DefaultMaxCategories is how many categories (the default ones included)
// one user may hold: well above any household's needs, and a bound against
// abuse.
const DefaultMaxCategories = 200

// Service serves the caller's categories.
type Service struct {
	store       Store
	marker      SeedMarker
	usage       Usage
	recurrences Usage
	invalidate  Invalidator
	now         func() time.Time
	max         int
}

// Options tunes the Service.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
	// MaxCategories defaults to DefaultMaxCategories.
	MaxCategories int
	// Recurrences are the other records that name categories; nil names
	// none.
	Recurrences Usage
	// Invalidator is told when a deletion moves transactions to another
	// category; it defaults to none.
	Invalidator Invalidator
}

type noInvalidator struct{}

func (noInvalidator) Invalidate(context.Context, string) {}

type noUsage struct{}

func (noUsage) CategoryInUse(context.Context, string, string) (bool, error) { return false, nil }
func (noUsage) ReassignCategory(context.Context, string, string, string, time.Time) error {
	return nil
}

// NewService returns the service.
func NewService(store Store, marker SeedMarker, usage Usage, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxCategories <= 0 {
		o.MaxCategories = DefaultMaxCategories
	}
	if o.Recurrences == nil {
		o.Recurrences = noUsage{}
	}
	if o.Invalidator == nil {
		o.Invalidator = noInvalidator{}
	}
	return &Service{
		store: store, marker: marker, usage: usage, recurrences: o.Recurrences, invalidate: o.Invalidator,
		now: o.Now, max: o.MaxCategories,
	}
}

// clock is the current time at the precision MongoDB keeps (milliseconds),
// so what a write returns equals what later reads return.
func (s *Service) clock() time.Time { return s.now().UTC().Truncate(time.Millisecond) }

// ensureDefaults gives the caller the default categories the first time
// they are read. It stores them before marking the user, and the store
// skips the ones already there, so a request that fails halfway is
// completed by the next one, and two concurrent first reads both see the
// whole set. Once marked, a default the owner deleted never comes back.
func (s *Service) ensureDefaults(ctx context.Context, userID string) error {
	seeded, err := s.marker.CategoriesSeeded(ctx, userID)
	if err != nil {
		return fmt.Errorf("check default categories: %w", err)
	}
	if seeded {
		return nil
	}
	now := s.clock()
	if err := s.store.InsertDefaults(ctx, domain.NewDefaultCategories(userID, now)); err != nil {
		return fmt.Errorf("create default categories: %w", err)
	}
	if err := s.marker.MarkCategoriesSeeded(ctx, userID, now); err != nil {
		return fmt.Errorf("mark default categories: %w", err)
	}
	return nil
}

// List returns the caller's categories, oldest first (the defaults in their
// curated order); only those of kind, unless kind is empty.
func (s *Service) List(ctx context.Context, userID string, kind domain.CategoryKind) ([]domain.Category, error) {
	if err := s.ensureDefaults(ctx, userID); err != nil {
		return nil, err
	}
	list, err := s.store.List(ctx, userID, kind)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	return list, nil
}

// Create validates and stores a new category. The defaults come first, so
// they count toward MaxCategories; past it, it answers
// domain.ErrLimitReached. Two creates at the same instant may both pass the
// count, which still bounds abuse.
func (s *Service) Create(ctx context.Context, userID string, in domain.CategoryInput) (domain.Category, error) {
	c, err := domain.NewCategory(userID, in, s.clock())
	if err != nil {
		return domain.Category{}, err
	}
	if err := s.ensureDefaults(ctx, userID); err != nil {
		return domain.Category{}, err
	}
	n, err := s.store.Count(ctx, userID)
	if err != nil {
		return domain.Category{}, fmt.Errorf("count categories: %w", err)
	}
	if n >= s.max {
		return domain.Category{}, domain.ErrLimitReached
	}
	created, err := s.store.Insert(ctx, c)
	if err != nil {
		return domain.Category{}, fmt.Errorf("create category: %w", err)
	}
	return created, nil
}

// Get returns one of the caller's categories, or domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, userID, id string) (domain.Category, error) {
	c, err := s.store.ByID(ctx, userID, id)
	if err != nil {
		return domain.Category{}, fmt.Errorf("find category: %w", err)
	}
	return c, nil
}

// Update applies a patch to one of the caller's categories and returns it.
// A patch that changes nothing writes nothing.
func (s *Service) Update(ctx context.Context, userID, id string, p domain.CategoryPatch) (domain.Category, error) {
	current, err := s.Get(ctx, userID, id)
	if err != nil {
		return domain.Category{}, err
	}
	next, changed, err := p.Apply(current, s.clock())
	if err != nil {
		return domain.Category{}, err
	}
	if !changed {
		return current, nil
	}
	if err := s.store.Update(ctx, next); err != nil {
		return domain.Category{}, fmt.Errorf("update category: %w", err)
	}
	return next, nil
}

// Delete removes one of the caller's categories, or answers
// domain.ErrNotFound. A deleted default is not seeded again.
//
// A category with transactions answers domain.ErrCategoryInUse, and one a
// recurrence names domain.ErrUsedByRecurrence, unless reassignTo names
// another of the caller's categories, of the same kind, to move them (the
// transactions and the recurrences) to first (else a
// *domain.ValidationError on reassignTo). The move runs again after the
// deletion, so a record created in between is not left naming a category
// that is gone.
func (s *Service) Delete(ctx context.Context, userID, id, reassignTo string) error {
	c, err := s.Get(ctx, userID, id)
	if err != nil {
		return err
	}
	if reassignTo == "" {
		for _, check := range []struct {
			usage Usage
			err   error
		}{{s.usage, domain.ErrCategoryInUse}, {s.recurrences, domain.ErrUsedByRecurrence}} {
			inUse, err := check.usage.CategoryInUse(ctx, userID, id)
			if err != nil {
				return fmt.Errorf("check category use: %w", err)
			}
			if inUse {
				return check.err
			}
		}
		return s.delete(ctx, userID, id)
	}

	if err := s.checkTarget(ctx, c, reassignTo); err != nil {
		return err
	}
	// Moving transactions changes the dashboard's categories; a category
	// deleted unused, or created or renamed, changes no number there.
	defer s.invalidate.Invalidate(ctx, userID)
	now := s.clock()
	if err := s.reassign(ctx, userID, id, reassignTo, now); err != nil {
		return err
	}
	if err := s.delete(ctx, userID, id); err != nil {
		return err
	}
	if err := s.reassign(ctx, userID, id, reassignTo, now); err != nil {
		return fmt.Errorf("move late records: %w", err)
	}
	return nil
}

// reassign moves the transactions and the recurrences of category from to
// category to.
func (s *Service) reassign(ctx context.Context, userID, from, to string, at time.Time) error {
	if err := s.usage.ReassignCategory(ctx, userID, from, to, at); err != nil {
		return fmt.Errorf("move transactions: %w", err)
	}
	if err := s.recurrences.ReassignCategory(ctx, userID, from, to, at); err != nil {
		return fmt.Errorf("move recurrences: %w", err)
	}
	return nil
}

// checkTarget checks that the transactions of c may move to category to.
func (s *Service) checkTarget(ctx context.Context, c domain.Category, to string) error {
	refuse := func(code string) error {
		return &domain.ValidationError{Fields: []domain.FieldError{{Field: "reassignTo", Code: code}}}
	}
	if to == c.ID {
		return refuse(domain.CodeInvalid)
	}
	target, err := s.store.ByID(ctx, c.UserID, to)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return refuse(domain.CodeInvalid)
	case err != nil:
		return fmt.Errorf("find category: %w", err)
	case target.Kind != c.Kind:
		return refuse(domain.CodeKindMismatch)
	}
	return nil
}

func (s *Service) delete(ctx context.Context, userID, id string) error {
	if err := s.store.Delete(ctx, userID, id); err != nil {
		return fmt.Errorf("delete category: %w", err)
	}
	return nil
}
