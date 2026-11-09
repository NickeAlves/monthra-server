package budgets

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// t0 is 2026-11-09 in UTC.
var t0 = time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)

var (
	november = domain.NewMonth(2026, time.November)
	errBoom  = errors.New("boom")
)

func day(t *testing.T, s string) domain.Date {
	t.Helper()
	d, err := domain.ParseDate(s)
	require.NoError(t, err)
	return d
}

func month(t *testing.T, s string) domain.Month {
	t.Helper()
	m, err := domain.ParseMonth(s)
	require.NoError(t, err)
	return m
}

// memStore keeps budgets in memory with the unique index of MongoDB: one
// per user, month and category.
type memStore struct {
	mu      sync.Mutex
	byID    map[string]domain.Budget
	order   []string
	seq     int
	lists   int
	pending int
	listErr error
	rollErr error
	claims  int
}

func newMemStore() *memStore { return &memStore{byID: map[string]domain.Budget{}} }

func (s *memStore) taken(b domain.Budget) bool {
	for _, o := range s.byID {
		if o.UserID == b.UserID && o.Month == b.Month && o.CategoryID == b.CategoryID {
			return true
		}
	}
	return false
}

func (s *memStore) add(b domain.Budget) domain.Budget {
	s.seq++
	b.ID = fmt.Sprintf("b%d", s.seq)
	s.byID[b.ID] = b
	s.order = append(s.order, b.ID)
	return b
}

func (s *memStore) inMonth(userID string, m domain.Month, keep func(domain.Budget) bool) []domain.Budget {
	var out []domain.Budget
	for _, id := range s.order {
		if b, ok := s.byID[id]; ok && b.UserID == userID && b.Month == m && keep(b) {
			out = append(out, b)
		}
	}
	return out
}

func (s *memStore) List(_ context.Context, userID string, m domain.Month) ([]domain.Budget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists++
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.inMonth(userID, m, func(domain.Budget) bool { return true }), nil
}

func (s *memStore) Pending(_ context.Context, userID string, m domain.Month) ([]domain.Budget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending++
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.inMonth(userID, m, func(b domain.Budget) bool { return b.ExceededAt == nil }), nil
}

func (s *memStore) ByID(_ context.Context, userID, id string) (domain.Budget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.byID[id]
	if !ok || b.UserID != userID {
		return domain.Budget{}, domain.ErrNotFound
	}
	return b, nil
}

func (s *memStore) Insert(_ context.Context, b domain.Budget) (domain.Budget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.taken(b) {
		return domain.Budget{}, domain.ErrBudgetExists
	}
	return s.add(b), nil
}

func (s *memStore) InsertRolled(_ context.Context, list []domain.Budget) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rollErr != nil {
		return s.rollErr
	}
	for _, b := range list {
		if !s.taken(b) {
			s.add(b)
		}
	}
	return nil
}

func (s *memStore) Update(_ context.Context, b domain.Budget) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.byID[b.ID]
	if !ok || cur.UserID != b.UserID {
		return domain.ErrNotFound
	}
	cur.Limit, cur.WarnAt, cur.WarnedAt, cur.ExceededAt, cur.UpdatedAt = b.Limit, b.WarnAt, b.WarnedAt, b.ExceededAt, b.UpdatedAt
	s.byID[b.ID] = cur
	return nil
}

func (s *memStore) ClaimAlert(_ context.Context, userID, id string, status domain.BudgetStatus, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	b, ok := s.byID[id]
	if !ok || b.UserID != userID {
		return false, nil
	}
	at = at.UTC()
	switch status {
	case domain.BudgetWarning:
		if b.WarnedAt != nil {
			return false, nil
		}
		b.WarnedAt = &at
	case domain.BudgetExceeded:
		if b.ExceededAt != nil {
			return false, nil
		}
		b.ExceededAt = &at
		if b.WarnedAt == nil {
			b.WarnedAt = &at
		}
	default:
		return false, errBoom
	}
	s.byID[id] = b
	return true, nil
}

func (s *memStore) Delete(_ context.Context, userID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.byID[id]
	if !ok || b.UserID != userID {
		return domain.ErrNotFound
	}
	delete(s.byID, id)
	return nil
}

// months returns the months userID has budgets in, sorted.
func (s *memStore) months(userID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, b := range s.byID {
		if b.UserID == userID && !slices.Contains(out, b.Month.String()) {
			out = append(out, b.Month.String())
		}
	}
	slices.Sort(out)
	return out
}

type memMarker struct {
	mu      sync.Mutex
	through map[string]domain.Month
	err     error
	marks   int
}

func newMemMarker() *memMarker { return &memMarker{through: map[string]domain.Month{}} }

func (m *memMarker) BudgetsRolledThrough(_ context.Context, userID string) (domain.Month, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.through[userID], m.err
}

func (m *memMarker) MarkBudgetsRolled(_ context.Context, userID string, month domain.Month) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marks++
	if m.through[userID].Before(month) {
		m.through[userID] = month
	}
	return nil
}

type fakeCategories map[string]domain.Category

func (f fakeCategories) ByID(_ context.Context, userID, id string) (domain.Category, error) {
	c, ok := f[id]
	if !ok || c.UserID != userID {
		return domain.Category{}, domain.ErrNotFound
	}
	return c, nil
}

type fakeLedger struct {
	mu    sync.Mutex
	flows []domain.FlowSum
	err   error
	calls []string
}

func (f *fakeLedger) Flows(_ context.Context, userID string, from, to domain.Date) ([]domain.FlowSum, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("%s %s %s", userID, from, to))
	return f.flows, f.err
}

// spend adds an expense sum of category in BRL in m.
func (f *fakeLedger) spend(m domain.Month, category string, amount int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flows = append(f.flows, domain.FlowSum{Month: m, Currency: "BRL", Kind: domain.TransactionExpense, CategoryID: category, Amount: amount, Count: 1})
}

type fakeRecurrences struct {
	scheduled    []domain.Transaction
	err          error
	materialized []string
}

func (f *fakeRecurrences) Materialize(_ context.Context, userID string, asOf domain.Date) {
	f.materialized = append(f.materialized, userID+" "+asOf.String())
}

func (f *fakeRecurrences) Scheduled(context.Context, string, domain.Date, domain.Date) ([]domain.Transaction, error) {
	return f.scheduled, f.err
}
