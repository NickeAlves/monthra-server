package accounts

import (
	"context"
	"errors"
	"fmt"
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

func newService(store Store, max int) *Service {
	return NewService(store, Options{Now: func() time.Time { return t0 }, MaxAccounts: max})
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

	got, err := svc.Get(context.Background(), "u1", a.ID)
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
	_, err := svc.Update(ctx, "u1", "a1", domain.AccountPatch{Archived: ptr(true)})
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
	_, err = svc.Update(ctx, "u1", "a2", domain.AccountPatch{Archived: ptr(true)})
	require.NoError(t, err)

	active, err := svc.List(ctx, "u1", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "C"}, names(active))

	all, err := svc.List(ctx, "u1", true)
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "B", "C"}, names(all))

	store.err = errors.New("mongo down")
	_, err = svc.List(ctx, "u1", true)
	assert.ErrorIs(t, err, store.err)
}

func names(list []domain.Account) []string {
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
	updated, err := svc.Update(ctx, "u1", a.ID, domain.AccountPatch{Name: ptr("Bank"), InitialBalance: ptr(int64(-5))})
	require.NoError(t, err)
	assert.Equal(t, "Bank", updated.Name)
	assert.Equal(t, int64(-5), updated.InitialBalance)
	assert.Equal(t, later.Truncate(time.Millisecond), updated.UpdatedAt)
	assert.Equal(t, updated, store.accounts[a.ID])
	assert.Equal(t, 1, store.updates)

	same, err := svc.Update(ctx, "u1", a.ID, domain.AccountPatch{Name: ptr("Bank")})
	require.NoError(t, err)
	assert.Equal(t, updated, same)
	assert.Equal(t, 1, store.updates, "nothing changed, nothing written")

	_, err = svc.Update(ctx, "u1", a.ID, domain.AccountPatch{Name: ptr("")})
	var ve *domain.ValidationError
	assert.ErrorAs(t, err, &ve)

	store.updateErr = domain.ErrNotFound // deleted between the read and the write
	_, err = svc.Update(ctx, "u1", a.ID, domain.AccountPatch{Name: ptr("Gone")})
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestAnotherUsersAccountIsNotFound(t *testing.T) {
	store := newMemStore()
	svc := newService(store, 0)
	ctx := context.Background()
	a, err := svc.Create(ctx, "u1", checking("Main"))
	require.NoError(t, err)

	_, err = svc.Get(ctx, "u2", a.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = svc.Update(ctx, "u2", a.ID, domain.AccountPatch{Name: ptr("Mine")})
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.ErrorIs(t, svc.Delete(ctx, "u2", a.ID), domain.ErrNotFound)
	assert.Equal(t, "Main", store.accounts[a.ID].Name, "untouched")

	require.NoError(t, svc.Delete(ctx, "u1", a.ID))
	assert.ErrorIs(t, svc.Delete(ctx, "u1", a.ID), domain.ErrNotFound)
}

func TestNewServiceDefaults(t *testing.T) {
	svc := NewService(newMemStore(), Options{})
	assert.Equal(t, DefaultMaxAccounts, svc.max)
	assert.WithinDuration(t, time.Now(), svc.now(), time.Minute)
}
