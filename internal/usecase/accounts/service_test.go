package accounts

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 123456789, time.UTC)

// memStore keeps accounts in memory, scoped by user like the real store.
type memStore struct {
	accounts  map[string]domain.Account
	next      int
	err       error
	updateErr error
	updates   int
}

func newMemStore() *memStore { return &memStore{accounts: map[string]domain.Account{}} }

func (m *memStore) owned(userID, id string) (domain.Account, error) {
	a, ok := m.accounts[id]
	if !ok || a.UserID != userID {
		return domain.Account{}, domain.ErrNotFound
	}
	return a, nil
}

func (m *memStore) List(_ context.Context, userID string, includeArchived bool) ([]domain.Account, error) {
	if m.err != nil {
		return nil, m.err
	}
	var out []domain.Account
	for i := range m.next {
		a, ok := m.accounts[fmt.Sprintf("a%d", i+1)]
		if ok && a.UserID == userID && (includeArchived || !a.Archived()) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m *memStore) Count(_ context.Context, userID string) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	n := 0
	for _, a := range m.accounts {
		if a.UserID == userID {
			n++
		}
	}
	return n, nil
}

func (m *memStore) Insert(_ context.Context, a domain.Account) (domain.Account, error) {
	m.next++
	a.ID = fmt.Sprintf("a%d", m.next)
	m.accounts[a.ID] = a
	return a, nil
}

func (m *memStore) ByID(_ context.Context, userID, id string) (domain.Account, error) {
	if m.err != nil {
		return domain.Account{}, m.err
	}
	return m.owned(userID, id)
}

func (m *memStore) Update(_ context.Context, a domain.Account) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	if _, err := m.owned(a.UserID, a.ID); err != nil {
		return err
	}
	m.updates++
	m.accounts[a.ID] = a
	return nil
}

func (m *memStore) Delete(_ context.Context, userID, id string) error {
	if _, err := m.owned(userID, id); err != nil {
		return err
	}
	delete(m.accounts, id)
	return nil
}

// fakeLedger answers fixed sums and usage, and records what it was asked.
type fakeLedger struct {
	sums   map[string]domain.Balance
	inUse  map[string]bool
	err    error
	calls  int
	ids    []string
	asOf   domain.Date
	userID string
}

func (l *fakeLedger) Balances(_ context.Context, userID string, ids []string, asOf domain.Date) (map[string]domain.Balance, error) {
	l.calls++
	l.userID, l.ids, l.asOf = userID, ids, asOf
	if l.err != nil {
		return nil, l.err
	}
	out := map[string]domain.Balance{}
	for id, b := range l.sums {
		if ids == nil || slices.Contains(ids, id) {
			out[id] = b
		}
	}
	return out, nil
}

func (l *fakeLedger) AccountInUse(_ context.Context, userID, id string) (bool, error) {
	l.userID = userID
	return l.inUse[id], l.err
}

func newService(store Store, max int) *Service {
	return NewService(store, &fakeLedger{}, Options{Now: func() time.Time { return t0 }, MaxAccounts: max})
}

func checking(name string) domain.AccountInput {
	return domain.AccountInput{Name: name, Type: "checking", Currency: "BRL", InitialBalance: 10000}
}

func ptr[T any](v T) *T { return &v }

func TestCreateAndGet(t *testing.T) {
	store := newMemStore()
	svc := newService(store, 0)

	a, err := svc.Create(context.Background(), "u1", checking("Main"))
	require.NoError(t, err)
	assert.Equal(t, "a1", a.ID)
	assert.Equal(t, "u1", a.UserID)
	assert.Equal(t, t0.Truncate(time.Millisecond), a.CreatedAt, "MongoDB precision")

	assert.Equal(t, domain.Balance{Current: 10000, Projected: 10000}, a.Balance, "a new account holds its initial balance")

	got, err := svc.Get(context.Background(), "u1", a.ID, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, a, got)
}

func TestCreateValidatesBeforeCounting(t *testing.T) {
	store := newMemStore()
	store.err = errors.New("must not be called")
	_, err := newService(store, 0).Create(context.Background(), "u1", domain.AccountInput{})
	var ve *domain.ValidationError
	assert.ErrorAs(t, err, &ve)
}

func TestCreateStopsAtTheLimit(t *testing.T) {
	store := newMemStore()
	svc := newService(store, 2)
	ctx := context.Background()
	for _, name := range []string{"A", "B"} {
		_, err := svc.Create(ctx, "u1", checking(name))
		require.NoError(t, err)
	}
	_, err := svc.Update(ctx, "u1", "a1", domain.AccountPatch{Archived: ptr(true)}, domain.Date{})
	require.NoError(t, err)

	_, err = svc.Create(ctx, "u1", checking("C"))
	assert.ErrorIs(t, err, domain.ErrLimitReached, "archived accounts count")

	_, err = svc.Create(ctx, "u2", checking("C"))
	assert.NoError(t, err, "the limit is per user")
}

func TestCreateReportsStoreFailures(t *testing.T) {
	store := newMemStore()
	store.err = errors.New("mongo down")
	_, err := newService(store, 0).Create(context.Background(), "u1", checking("Main"))
	assert.ErrorIs(t, err, store.err)
}

func TestList(t *testing.T) {
	store := newMemStore()
	svc := newService(store, 0)
	ctx := context.Background()
	for _, name := range []string{"A", "B", "C"} {
		_, err := svc.Create(ctx, "u1", checking(name))
		require.NoError(t, err)
	}
	_, err := svc.Create(ctx, "u2", checking("Other"))
	require.NoError(t, err)
	_, err = svc.Update(ctx, "u1", "a2", domain.AccountPatch{Archived: ptr(true)}, domain.Date{})
	require.NoError(t, err)

	active, err := svc.List(ctx, "u1", false, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "C"}, names(active))

	all, err := svc.List(ctx, "u1", true, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "B", "C"}, names(all))

	store.err = errors.New("mongo down")
	_, err = svc.List(ctx, "u1", true, domain.Date{})
	assert.ErrorIs(t, err, store.err)
}

func TestBalances(t *testing.T) {
	store := newMemStore()
	ledger := &fakeLedger{sums: map[string]domain.Balance{"a1": {Current: -500, Projected: -800}}}
	svc := NewService(store, ledger, Options{Now: func() time.Time { return t0 }})
	ctx := context.Background()

	empty, err := svc.List(ctx, "u1", false, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []domain.AccountWithBalance{}, empty, "never null")
	assert.Zero(t, ledger.calls, "no accounts, nothing to sum")

	for _, name := range []string{"A", "B"} {
		_, err := svc.Create(ctx, "u1", checking(name))
		require.NoError(t, err)
	}
	assert.Zero(t, ledger.calls, "a new account has no transactions to sum")

	list, err := svc.List(ctx, "u1", false, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, domain.Balance{Current: 9500, Projected: 9200}, list[0].Balance, "initial balance plus the sums")
	assert.Equal(t, domain.Balance{Current: 10000, Projected: 10000}, list[1].Balance, "no transactions")
	assert.Equal(t, "2026-11-08", ledger.asOf.String(), "today in UTC by default")
	assert.Nil(t, ledger.ids, "every account of the caller in one sum")
	assert.Equal(t, "u1", ledger.userID)

	asOf, err := domain.ParseDate("2026-11-30")
	require.NoError(t, err)
	got, err := svc.Get(ctx, "u1", "a1", asOf)
	require.NoError(t, err)
	assert.Equal(t, domain.Balance{Current: 9500, Projected: 9200}, got.Balance)
	assert.Equal(t, []string{"a1"}, ledger.ids)
	assert.Equal(t, asOf, ledger.asOf, "the reader's today")

	updated, err := svc.Update(ctx, "u1", "a1", domain.AccountPatch{InitialBalance: ptr(int64(0))}, asOf)
	require.NoError(t, err)
	assert.Equal(t, domain.Balance{Current: -500, Projected: -800}, updated.Balance)
	same, err := svc.Update(ctx, "u1", "a1", domain.AccountPatch{}, asOf)
	require.NoError(t, err)
	assert.Equal(t, updated, same, "an empty patch still answers the balance")

	ledger.err = errors.New("mongo down")
	_, err = svc.List(ctx, "u1", false, asOf)
	assert.ErrorIs(t, err, ledger.err)
	_, err = svc.Get(ctx, "u1", "a1", asOf)
	assert.ErrorIs(t, err, ledger.err)
}

func TestDeleteRefusesAnAccountInUse(t *testing.T) {
	store := newMemStore()
	ledger := &fakeLedger{inUse: map[string]bool{"a1": true}}
	svc := NewService(store, ledger, Options{})
	ctx := context.Background()
	for _, name := range []string{"A", "B"} {
		_, err := svc.Create(ctx, "u1", checking(name))
		require.NoError(t, err)
	}

	assert.ErrorIs(t, svc.Delete(ctx, "u1", "a1"), domain.ErrAccountInUse)
	assert.Contains(t, store.accounts, "a1", "kept: archive it instead")
	require.NoError(t, svc.Delete(ctx, "u1", "a2"))
	assert.NotContains(t, store.accounts, "a2")

	ledger.err = errors.New("mongo down")
	assert.ErrorIs(t, svc.Delete(ctx, "u1", "a1"), ledger.err)
}

func names(list []domain.AccountWithBalance) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.Name
	}
	return out
}

func TestUpdate(t *testing.T) {
	store := newMemStore()
	svc := newService(store, 0)
	ctx := context.Background()
	a, err := svc.Create(ctx, "u1", checking("Main"))
	require.NoError(t, err)

	later := t0.Add(time.Hour)
	svc.now = func() time.Time { return later }
	updated, err := svc.Update(ctx, "u1", a.ID, domain.AccountPatch{Name: ptr("Bank"), InitialBalance: ptr(int64(-5))}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, "Bank", updated.Name)
	assert.Equal(t, int64(-5), updated.InitialBalance)
	assert.Equal(t, later.Truncate(time.Millisecond), updated.UpdatedAt)
	assert.Equal(t, updated.Account, store.accounts[a.ID])
	assert.Equal(t, 1, store.updates)

	same, err := svc.Update(ctx, "u1", a.ID, domain.AccountPatch{Name: ptr("Bank")}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, updated, same)
	assert.Equal(t, 1, store.updates, "nothing changed, nothing written")

	_, err = svc.Update(ctx, "u1", a.ID, domain.AccountPatch{Name: ptr("")}, domain.Date{})
	var ve *domain.ValidationError
	assert.ErrorAs(t, err, &ve)

	store.updateErr = domain.ErrNotFound // deleted between the read and the write
	_, err = svc.Update(ctx, "u1", a.ID, domain.AccountPatch{Name: ptr("Gone")}, domain.Date{})
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestAnotherUsersAccountIsNotFound(t *testing.T) {
	store := newMemStore()
	svc := newService(store, 0)
	ctx := context.Background()
	a, err := svc.Create(ctx, "u1", checking("Main"))
	require.NoError(t, err)

	_, err = svc.Get(ctx, "u2", a.ID, domain.Date{})
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = svc.Update(ctx, "u2", a.ID, domain.AccountPatch{Name: ptr("Mine")}, domain.Date{})
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.ErrorIs(t, svc.Delete(ctx, "u2", a.ID), domain.ErrNotFound)
	assert.Equal(t, "Main", store.accounts[a.ID].Name, "untouched")

	require.NoError(t, svc.Delete(ctx, "u1", a.ID))
	assert.ErrorIs(t, svc.Delete(ctx, "u1", a.ID), domain.ErrNotFound)
}

func TestNewServiceDefaults(t *testing.T) {
	svc := NewService(newMemStore(), &fakeLedger{}, Options{})
	assert.Equal(t, DefaultMaxAccounts, svc.max)
	assert.WithinDuration(t, time.Now(), svc.now(), time.Minute)
}
