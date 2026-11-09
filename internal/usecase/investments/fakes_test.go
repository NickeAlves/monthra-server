package investments

import (
	"cmp"
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

var errBoom = errors.New("boom")

// memStore keeps investments in memory, scoped by user like MongoDB.
type memStore struct {
	mu       sync.Mutex
	byID     map[string]domain.Investment
	seq      int
	count    *int
	deletes  int
	writes   int
	countErr error
}

func newMemStore() *memStore { return &memStore{byID: map[string]domain.Investment{}} }

func (s *memStore) List(_ context.Context, userID string) ([]domain.Investment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Investment
	for _, inv := range s.byID {
		if inv.UserID == userID {
			out = append(out, inv)
		}
	}
	slices.SortFunc(out, func(a, b domain.Investment) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func (s *memStore) Count(_ context.Context, userID string) (int, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	if s.count != nil {
		return *s.count, nil
	}
	list, _ := s.List(context.Background(), userID)
	return len(list), nil
}

func (s *memStore) Insert(_ context.Context, inv domain.Investment) (domain.Investment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	inv.ID = fmt.Sprintf("i%04d", s.seq)
	s.byID[inv.ID] = inv
	s.writes++
	return inv, nil
}

func (s *memStore) ByID(_ context.Context, userID, id string) (domain.Investment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.byID[id]
	if !ok || inv.UserID != userID {
		return domain.Investment{}, domain.ErrNotFound
	}
	return inv, nil
}

func (s *memStore) Update(_ context.Context, inv domain.Investment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.byID[inv.ID]
	if !ok || cur.UserID != inv.UserID {
		return domain.ErrNotFound
	}
	s.byID[inv.ID] = inv
	s.writes++
	return nil
}

func (s *memStore) Delete(_ context.Context, userID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.byID[id]
	if !ok || inv.UserID != userID {
		return domain.ErrNotFound
	}
	delete(s.byID, id)
	s.deletes++
	return nil
}

// memMovements keeps movements in memory, scoped by user and investment.
type memMovements struct {
	mu        sync.Mutex
	byID      map[string]domain.Movement
	seq       int
	count     *int
	writes    int
	deleteOfs int
	pageErr   error
	allErr    error
}

func newMemMovements() *memMovements { return &memMovements{byID: map[string]domain.Movement{}} }

func (s *memMovements) of(userID, invID string) []domain.Movement {
	var out []domain.Movement
	for _, m := range s.byID {
		if m.UserID == userID && m.InvestmentID == invID {
			out = append(out, m)
		}
	}
	// Newest first, as MongoDB returns them.
	slices.SortFunc(out, func(a, b domain.Movement) int {
		if c := b.Date.Time().Compare(a.Date.Time()); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	return out
}

func (s *memMovements) Of(_ context.Context, userID, invID string) ([]domain.Movement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.of(userID, invID), nil
}

func (s *memMovements) All(_ context.Context, userID string) (map[string][]domain.Movement, error) {
	if s.allErr != nil {
		return nil, s.allErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]domain.Movement{}
	for _, m := range s.byID {
		if m.UserID == userID {
			out[m.InvestmentID] = append(out[m.InvestmentID], m)
		}
	}
	return out, nil
}

func (s *memMovements) Page(_ context.Context, userID, invID string, f domain.MovementFilter, after *domain.MovementCursor, limit int) ([]domain.Movement, error) {
	if s.pageErr != nil {
		return nil, s.pageErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Movement
	for _, m := range s.of(userID, invID) {
		if f.Type != "" && m.Type != f.Type {
			continue
		}
		if after != nil && !before(m, *after) {
			continue
		}
		out = append(out, m)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// before reports whether m comes after the cursor in the listing order
// (newest first).
func before(m domain.Movement, c domain.MovementCursor) bool {
	if m.Date.Equal(c.Date) {
		return m.ID < c.ID
	}
	return m.Date.Before(c.Date)
}

func (s *memMovements) ByID(_ context.Context, userID, invID, id string) (domain.Movement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byID[id]
	if !ok || m.UserID != userID || m.InvestmentID != invID {
		return domain.Movement{}, domain.ErrNotFound
	}
	return m, nil
}

func (s *memMovements) Count(_ context.Context, userID, invID string) (int, error) {
	if s.count != nil {
		return *s.count, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.of(userID, invID)), nil
}

func (s *memMovements) Insert(_ context.Context, m domain.Movement) (domain.Movement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	m.ID = fmt.Sprintf("m%04d", s.seq)
	s.byID[m.ID] = m
	s.writes++
	return m, nil
}

func (s *memMovements) Update(_ context.Context, m domain.Movement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.byID[m.ID]
	if !ok || cur.UserID != m.UserID || cur.InvestmentID != m.InvestmentID {
		return domain.ErrNotFound
	}
	s.byID[m.ID] = m
	s.writes++
	return nil
}

func (s *memMovements) Delete(_ context.Context, userID, invID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byID[id]
	if !ok || m.UserID != userID || m.InvestmentID != invID {
		return domain.ErrNotFound
	}
	delete(s.byID, id)
	s.writes++
	return nil
}

func (s *memMovements) DeleteOf(_ context.Context, userID, invID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, m := range s.byID {
		if m.UserID == userID && m.InvestmentID == invID {
			delete(s.byID, id)
		}
	}
	s.deleteOfs++
	return nil
}

// refs finds accounts and categories by id, scoped by user.
type refs struct {
	accounts   map[string]domain.Account
	categories map[string]domain.Category
	err        error
}

func (r refs) find(userID, id string) (any, error) {
	if r.err != nil {
		return nil, r.err
	}
	if a, ok := r.accounts[id]; ok && a.UserID == userID {
		return a, nil
	}
	if c, ok := r.categories[id]; ok && c.UserID == userID {
		return c, nil
	}
	return nil, domain.ErrNotFound
}

type fakeAccounts struct{ refs }

func (f fakeAccounts) ByID(_ context.Context, userID, id string) (domain.Account, error) {
	v, err := f.find(userID, id)
	if err != nil {
		return domain.Account{}, err
	}
	a, ok := v.(domain.Account)
	if !ok {
		return domain.Account{}, domain.ErrNotFound
	}
	return a, nil
}

type fakeCategories struct{ refs }

func (f fakeCategories) ByID(_ context.Context, userID, id string) (domain.Category, error) {
	v, err := f.find(userID, id)
	if err != nil {
		return domain.Category{}, err
	}
	c, ok := v.(domain.Category)
	if !ok {
		return domain.Category{}, domain.ErrNotFound
	}
	return c, nil
}

// hookCounter counts the write hooks run, by user.
type hookCounter struct {
	mu    sync.Mutex
	calls []string
}

func (h *hookCounter) AfterWrite(_ context.Context, userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, userID)
}

func (h *hookCounter) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

type metricsCounter struct{ created []string }

func (m *metricsCounter) MovementCreated(t string) { m.created = append(m.created, t) }

// fixture is a service on in-memory stores with a brokerage account, a card,
// an archived account, a USD account and income and expense categories of
// user u1.
type fixture struct {
	svc       *Service
	store     *memStore
	movements *memMovements
	hooks     *hookCounter
	metrics   *metricsCounter
	refs      *refs
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	archived := t0
	r := &refs{
		accounts: map[string]domain.Account{
			"broker":   {ID: "broker", UserID: "u1", Type: domain.AccountBrokerage, Currency: "BRL"},
			"bank":     {ID: "bank", UserID: "u1", Type: domain.AccountChecking, Currency: "BRL"},
			"card":     {ID: "card", UserID: "u1", Type: domain.AccountCreditCard, Currency: "BRL"},
			"old":      {ID: "old", UserID: "u1", Type: domain.AccountBrokerage, Currency: "BRL", ArchivedAt: &archived},
			"dollars":  {ID: "dollars", UserID: "u1", Type: domain.AccountBrokerage, Currency: "USD"},
			"theirs":   {ID: "theirs", UserID: "u2", Type: domain.AccountBrokerage, Currency: "BRL"},
			"elsewhen": {ID: "elsewhen", UserID: "u1", Type: domain.AccountCash, Currency: "BRL"},
		},
		categories: map[string]domain.Category{
			"dividends": {ID: "dividends", UserID: "u1", Kind: domain.CategoryIncome},
			"rent":      {ID: "rent", UserID: "u1", Kind: domain.CategoryIncome},
			"food":      {ID: "food", UserID: "u1", Kind: domain.CategoryExpense},
		},
	}
	f := &fixture{store: newMemStore(), movements: newMemMovements(), hooks: &hookCounter{}, metrics: &metricsCounter{}, refs: r}
	f.svc = NewService(f.store, f.movements, fakeAccounts{*r}, fakeCategories{*r}, Options{
		Now: func() time.Time { return t0 }, AfterWrite: f.hooks, Metrics: f.metrics,
	})
	return f
}

// invest creates an investment of u1.
func (f *fixture) invest(t *testing.T, typ domain.InvestmentType, mode domain.InvestmentMode) domain.Investment {
	t.Helper()
	ip, err := f.svc.Create(context.Background(), "u1", domain.InvestmentInput{
		Name: string(typ), Type: string(typ), Mode: string(mode), Currency: "BRL",
	})
	require.NoError(t, err)
	return ip.Investment
}

// move creates a movement of inv.
func (f *fixture) move(t *testing.T, inv domain.Investment, in domain.MovementInput) domain.Movement {
	t.Helper()
	m, err := f.svc.CreateMovement(context.Background(), "u1", inv.ID, in, domain.Date{})
	require.NoError(t, err)
	return m
}

func amount(v int64) *int64 { return &v }

func ptr[T any](v T) *T { return &v }

// fieldsOf maps a *domain.ValidationError to field -> code.
func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *domain.ValidationError
	require.ErrorAs(t, err, &ve)
	out := map[string]string{}
	for _, f := range ve.Fields {
		out[f.Field] = f.Code
	}
	return out
}
