package investments

import (
	"context"
	"errors"
	"fmt"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Page sizes of ListMovements.
const (
	DefaultPageSize = 50
	MaxPageSize     = 100
)

// MovementQuery is one page of an investment's movements: the filter, where
// the previous page ended (nil for the first one) and the page size.
type MovementQuery struct {
	Filter domain.MovementFilter
	After  *domain.MovementCursor
	// Limit is between 1 and MaxPageSize; 0 means DefaultPageSize.
	Limit int
}

// MovementPage is a page of movements and where the next one starts, or nil
// on the last page.
type MovementPage struct {
	Items []domain.Movement
	Next  *domain.MovementCursor
}

// ListMovements returns a page of the movements of investment invID of the
// caller, newest first, or domain.ErrNotFound when the investment is not
// theirs.
func (s *Service) ListMovements(ctx context.Context, userID, invID string, q MovementQuery) (MovementPage, error) {
	limit := q.Limit
	switch {
	case limit == 0:
		limit = DefaultPageSize
	case limit < 0 || limit > MaxPageSize:
		return MovementPage{}, &domain.ValidationError{Fields: []domain.FieldError{{Field: "limit", Code: domain.CodeOutOfRange}}}
	}
	if _, err := s.store.ByID(ctx, userID, invID); err != nil {
		return MovementPage{}, err
	}
	// One more than the page tells whether another page follows.
	items, err := s.movements.Page(ctx, userID, invID, q.Filter, q.After, limit+1)
	if err != nil {
		return MovementPage{}, fmt.Errorf("list movements: %w", err)
	}
	page := MovementPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.Next = &domain.MovementCursor{Date: last.Date, ID: last.ID}
	}
	return page, nil
}

// GetMovement returns movement id of investment invID of the caller, or
// domain.ErrNotFound.
func (s *Service) GetMovement(ctx context.Context, userID, invID, id string) (domain.Movement, error) {
	if _, err := s.store.ByID(ctx, userID, invID); err != nil {
		return domain.Movement{}, err
	}
	return s.movements.ByID(ctx, userID, invID, id)
}

// CreateMovement validates and stores a new movement of investment invID of
// the caller for a reader whose today is asOf (their local day, within a
// day of today in UTC: domain.Today), and returns it. The category an
// income names is looked up among the caller's categories only. A movement
// that breaks the position is refused: a sale of more units than held on
// its day (quantity: exceeds_position), or a sum past its bounds (amount:
// out_of_range). Past domain.MaxMovementsPerInvestment it answers
// domain.ErrLimitReached.
func (s *Service) CreateMovement(ctx context.Context, userID, invID string, in domain.MovementInput, asOf domain.Date) (domain.Movement, error) {
	inv, err := s.store.ByID(ctx, userID, invID)
	if err != nil {
		return domain.Movement{}, err
	}
	n, err := s.movements.Count(ctx, userID, invID)
	if err != nil {
		return domain.Movement{}, fmt.Errorf("count movements: %w", err)
	}
	if n >= domain.MaxMovementsPerInvestment {
		return domain.Movement{}, domain.ErrLimitReached
	}
	var refs domain.MovementRefs
	if refs.Category, err = s.category(ctx, userID, in.CategoryID); err != nil {
		return domain.Movement{}, err
	}
	m, err := domain.NewMovement(inv, in, refs, domain.Today(asOf, s.now()), s.clock())
	if err != nil {
		return domain.Movement{}, err
	}
	existing, err := s.movements.Of(ctx, userID, invID)
	if err != nil {
		return domain.Movement{}, fmt.Errorf("list movements: %w", err)
	}
	if err := fits(inv.Mode, existing, append(existing, m)); err != nil {
		return domain.Movement{}, refusal(err)
	}
	if m.Type == domain.MovementIncome {
		defer s.afterWrite.AfterWrite(ctx, userID)
	}
	saved, err := s.movements.Insert(ctx, m)
	if err != nil {
		return domain.Movement{}, fmt.Errorf("store movement: %w", err)
	}
	s.metrics.MovementCreated(string(saved.Type))
	return saved, nil
}

// UpdateMovement applies p to movement id of investment invID of the caller
// for a reader whose today is asOf, and returns it. A patch that changes
// nothing writes nothing; one that breaks the position is refused, like a
// new movement.
func (s *Service) UpdateMovement(ctx context.Context, userID, invID, id string, p domain.MovementPatch, asOf domain.Date) (domain.Movement, error) {
	inv, err := s.store.ByID(ctx, userID, invID)
	if err != nil {
		return domain.Movement{}, err
	}
	current, err := s.movements.ByID(ctx, userID, invID, id)
	if err != nil {
		return domain.Movement{}, err
	}
	var refs domain.MovementRefs
	if p.CategoryID != nil && *p.CategoryID != current.CategoryID {
		if refs.Category, err = s.category(ctx, userID, *p.CategoryID); err != nil {
			return domain.Movement{}, err
		}
	}
	next, changed, err := p.Apply(current, refs, domain.Today(asOf, s.now()), s.clock())
	if err != nil {
		return domain.Movement{}, err
	}
	if !changed {
		return current, nil
	}
	existing, err := s.movements.Of(ctx, userID, invID)
	if err != nil {
		return domain.Movement{}, fmt.Errorf("list movements: %w", err)
	}
	if err := fits(inv.Mode, existing, replaced(existing, next)); err != nil {
		return domain.Movement{}, refusal(err)
	}
	if current.Type == domain.MovementIncome {
		defer s.afterWrite.AfterWrite(ctx, userID)
	}
	if err := s.movements.Update(ctx, next); err != nil {
		return domain.Movement{}, fmt.Errorf("update movement: %w", err)
	}
	return next, nil
}

// DeleteMovement removes movement id of investment invID of the caller, or
// answers domain.ErrExceedsPosition when that leaves a later sale with
// more units than held (a buy it relied on).
func (s *Service) DeleteMovement(ctx context.Context, userID, invID, id string) error {
	inv, err := s.store.ByID(ctx, userID, invID)
	if err != nil {
		return err
	}
	current, err := s.movements.ByID(ctx, userID, invID, id)
	if err != nil {
		return err
	}
	existing, err := s.movements.Of(ctx, userID, invID)
	if err != nil {
		return fmt.Errorf("list movements: %w", err)
	}
	if err := fits(inv.Mode, existing, without(existing, id)); err != nil {
		return domain.ErrExceedsPosition
	}
	if current.Type == domain.MovementIncome {
		defer s.afterWrite.AfterWrite(ctx, userID)
	}
	if err := s.movements.Delete(ctx, userID, invID, id); err != nil {
		return fmt.Errorf("delete movement: %w", err)
	}
	return nil
}

// fits tells whether the movements after a write still make a position,
// given those before it: a write is refused only for the error it brings.
// Records a race already broke (two sales at once) never block the write
// that mends them.
func fits(mode domain.InvestmentMode, before, after []domain.Movement) error {
	if _, err := domain.BuildPosition(mode, before); err != nil {
		return nil //nolint:nilerr // already broken: the write may mend it
	}
	_, err := domain.BuildPosition(mode, after)
	return err
}

// refusal is the validation error of a write fits refused.
func refusal(err error) error {
	field := domain.FieldError{Field: "amount", Code: domain.CodeOutOfRange}
	if errors.Is(err, domain.ErrExceedsPosition) {
		field = domain.FieldError{Field: "quantity", Code: domain.CodeExceedsPosition}
	}
	return &domain.ValidationError{Fields: []domain.FieldError{field}}
}

// replaced returns list with the movement of m's id replaced by m.
func replaced(list []domain.Movement, m domain.Movement) []domain.Movement {
	out := make([]domain.Movement, len(list))
	for i, x := range list {
		if x.ID == m.ID {
			x = m
		}
		out[i] = x
	}
	return out
}

// without returns list without the movement of id.
func without(list []domain.Movement, id string) []domain.Movement {
	out := make([]domain.Movement, 0, len(list))
	for _, x := range list {
		if x.ID != id {
			out = append(out, x)
		}
	}
	return out
}
