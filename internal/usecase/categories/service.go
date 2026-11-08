package categories

import (
	"context"
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
	store  Store
	marker SeedMarker
	now    func() time.Time
	max    int
}

// Options tunes the Service.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
	// MaxCategories defaults to DefaultMaxCategories.
	MaxCategories int
}

// NewService returns the service.
func NewService(store Store, marker SeedMarker, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxCategories <= 0 {
		o.MaxCategories = DefaultMaxCategories
	}
	return &Service{store: store, marker: marker, now: o.Now, max: o.MaxCategories}
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
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	if err := s.store.Delete(ctx, userID, id); err != nil {
		return fmt.Errorf("delete category: %w", err)
	}
	return nil
}
