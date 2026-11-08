package categories

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

// memStore keeps categories in memory, scoped by user like the real store,
// and skips a default its owner already holds, like the unique index.
type memStore struct {
	categories map[string]domain.Category
	next       int
	err        error
	defaultErr error
	updateErr  error
	updates    int
	seedCalls  int
}

func newMemStore() *memStore { return &memStore{categories: map[string]domain.Category{}} }

func (m *memStore) owned(userID, id string) (domain.Category, error) {
	c, ok := m.categories[id]
	if !ok || c.UserID != userID {
		return domain.Category{}, domain.ErrNotFound
	}
	return c, nil
}

func (m *memStore) List(_ context.Context, userID string, kind domain.CategoryKind) ([]domain.Category, error) {
	if m.err != nil {
		return nil, m.err
	}
	var out []domain.Category
	for i := range m.next {
		c, ok := m.categories[fmt.Sprintf("c%d", i+1)]
		if ok && c.UserID == userID && (kind == "" || c.Kind == kind) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (m *memStore) Count(_ context.Context, userID string) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	n := 0
	for _, c := range m.categories {
		if c.UserID == userID {
			n++
		}
	}
	return n, nil
}

func (m *memStore) Insert(_ context.Context, c domain.Category) (domain.Category, error) {
	if m.err != nil {
		return domain.Category{}, m.err
	}
	m.next++
	c.ID = fmt.Sprintf("c%d", m.next)
	m.categories[c.ID] = c
	return c, nil
}

func (m *memStore) InsertDefaults(ctx context.Context, list []domain.Category) error {
	m.seedCalls++
	if m.defaultErr != nil {
		return m.defaultErr
	}
	for _, c := range list {
		held := false
		for _, have := range m.categories {
			held = held || (have.UserID == c.UserID && have.DefaultKey == c.DefaultKey)
		}
		if !held {
			if _, err := m.Insert(ctx, c); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *memStore) ByID(_ context.Context, userID, id string) (domain.Category, error) {
	if m.err != nil {
		return domain.Category{}, m.err
	}
	return m.owned(userID, id)
}

func (m *memStore) Update(_ context.Context, c domain.Category) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	if _, err := m.owned(c.UserID, c.ID); err != nil {
		return err
	}
	m.updates++
	m.categories[c.ID] = c
	return nil
}

func (m *memStore) Delete(_ context.Context, userID, id string) error {
	if _, err := m.owned(userID, id); err != nil {
		return err
	}
	delete(m.categories, id)
	return nil
}

// memMarker records seeded users in memory.
type memMarker struct {
	seeded   map[string]time.Time
	checkErr error
	markErr  error
}

func newMemMarker() *memMarker { return &memMarker{seeded: map[string]time.Time{}} }

func (m *memMarker) CategoriesSeeded(_ context.Context, userID string) (bool, error) {
	if m.checkErr != nil {
		return false, m.checkErr
	}
	_, ok := m.seeded[userID]
	return ok, nil
}

func (m *memMarker) MarkCategoriesSeeded(_ context.Context, userID string, at time.Time) error {
	if m.markErr != nil {
		return m.markErr
	}
	if _, ok := m.seeded[userID]; !ok {
		m.seeded[userID] = at
	}
	return nil
}

// memUsage counts the transactions of each category (by id), like the
// transactions store.
type memUsage struct {
	byCategory map[string]int
	moves      []string
	err        error
	moveErr    error
	// onMove runs after each move, to create a transaction in between.
	onMove func()
}

func newMemUsage() *memUsage { return &memUsage{byCategory: map[string]int{}} }

func (u *memUsage) CategoryInUse(_ context.Context, _, id string) (bool, error) {
	return u.byCategory[id] > 0, u.err
}

func (u *memUsage) ReassignCategory(_ context.Context, _, from, to string, at time.Time) error {
	if u.moveErr != nil {
		return u.moveErr
	}
	u.moves = append(u.moves, fmt.Sprintf("%s->%s@%s", from, to, at.Format(time.StampMilli)))
	u.byCategory[to] += u.byCategory[from]
	delete(u.byCategory, from)
	if u.onMove != nil {
		u.onMove()
		u.onMove = nil
	}
	return nil
}

func newService(store Store, marker SeedMarker, max int) *Service {
	return NewService(store, marker, newMemUsage(), Options{Now: func() time.Time { return t0 }, MaxCategories: max})
}

func food() domain.CategoryInput {
	return domain.CategoryInput{Name: "Food", Kind: "expense", Icon: "utensils", Color: "#AA0000"}
}

func ptr[T any](v T) *T { return &v }

func keys(list []domain.Category) []string {
	out := make([]string, len(list))
	for i, c := range list {
		out[i] = c.DefaultKey
		if c.Name != "" {
			out[i] = c.Name
		}
	}
	return out
}

func defaultKeys() []string {
	return keys(domain.NewDefaultCategories("u1", t0))
}

func TestListSeedsTheDefaultsOnce(t *testing.T) {
	store, marker := newMemStore(), newMemMarker()
	svc := newService(store, marker, 0)
	ctx := context.Background()

	list, err := svc.List(ctx, "u1", "")
	require.NoError(t, err)
	assert.Equal(t, defaultKeys(), keys(list), "the defaults, in their curated order")
	assert.Equal(t, t0.Truncate(time.Millisecond), marker.seeded["u1"])
	assert.Equal(t, t0.Truncate(time.Millisecond), list[0].CreatedAt, "MongoDB precision")
	assert.Equal(t, "u1", list[0].UserID)

	again, err := svc.List(ctx, "u1", "")
	require.NoError(t, err)
	assert.Equal(t, list, again)
	assert.Equal(t, 1, store.seedCalls, "a seeded user is never seeded again")

	// A deleted default never comes back.
	require.NoError(t, svc.Delete(ctx, "u1", list[0].ID, ""))
	after, err := svc.List(ctx, "u1", "")
	require.NoError(t, err)
	assert.Equal(t, defaultKeys()[1:], keys(after))

	// Each user gets their own set.
	other, err := svc.List(ctx, "u2", "")
	require.NoError(t, err)
	assert.Len(t, other, len(defaultKeys()))
	assert.Equal(t, "u2", other[0].UserID)
}

func TestListFiltersByKind(t *testing.T) {
	svc := newService(newMemStore(), newMemMarker(), 0)
	ctx := context.Background()
	income, err := svc.List(ctx, "u1", domain.CategoryIncome)
	require.NoError(t, err)
	assert.Equal(t, []string{"salary", "side_income", "investment_income", "refunds", "gifts_received", "other_income"}, keys(income))

	expenses, err := svc.List(ctx, "u1", domain.CategoryExpense)
	require.NoError(t, err)
	assert.Len(t, expenses, 16)
}

func TestSeedingFailuresAreRetried(t *testing.T) {
	store, marker := newMemStore(), newMemMarker()
	svc := newService(store, marker, 0)
	ctx := context.Background()

	store.defaultErr = errors.New("mongo down")
	_, err := svc.List(ctx, "u1", "")
	require.ErrorIs(t, err, store.defaultErr)
	assert.NotContains(t, marker.seeded, "u1", "not marked when the defaults were not stored")

	// Stored but not marked: the next read completes it without duplicates.
	store.defaultErr = nil
	marker.markErr = errors.New("mongo down")
	_, err = svc.List(ctx, "u1", "")
	require.ErrorIs(t, err, marker.markErr)
	marker.markErr = nil
	list, err := svc.List(ctx, "u1", "")
	require.NoError(t, err)
	assert.Equal(t, defaultKeys(), keys(list))
	assert.Contains(t, marker.seeded, "u1")

	marker.checkErr = errors.New("mongo down")
	_, err = svc.List(ctx, "u1", "")
	assert.ErrorIs(t, err, marker.checkErr)
	_, err = svc.Create(ctx, "u1", food())
	assert.ErrorIs(t, err, marker.checkErr)
	marker.checkErr = nil

	store.err = errors.New("mongo down")
	_, err = svc.List(ctx, "u1", "")
	assert.ErrorIs(t, err, store.err)
}

func TestCreateAndGet(t *testing.T) {
	store, marker := newMemStore(), newMemMarker()
	svc := newService(store, marker, 0)
	ctx := context.Background()

	c, err := svc.Create(ctx, "u1", food())
	require.NoError(t, err)
	assert.Equal(t, "u1", c.UserID)
	assert.Equal(t, "Food", c.Name)
	assert.Empty(t, c.DefaultKey)
	assert.Contains(t, marker.seeded, "u1", "a create seeds the defaults first")

	got, err := svc.Get(ctx, "u1", c.ID)
	require.NoError(t, err)
	assert.Equal(t, c, got)

	list, err := svc.List(ctx, "u1", "")
	require.NoError(t, err)
	assert.Equal(t, append(defaultKeys(), "Food"), keys(list), "the defaults first, then the new one")
}

func TestCreateValidatesBeforeTouchingTheStore(t *testing.T) {
	store, marker := newMemStore(), newMemMarker()
	store.err = errors.New("must not be called")
	_, err := newService(store, marker, 0).Create(context.Background(), "u1", domain.CategoryInput{})
	var ve *domain.ValidationError
	assert.ErrorAs(t, err, &ve)
	assert.Zero(t, store.seedCalls)
}

func TestCreateStopsAtTheLimit(t *testing.T) {
	store, marker := newMemStore(), newMemMarker()
	n := len(defaultKeys())
	svc := newService(store, marker, n+2)
	ctx := context.Background()
	for range 2 {
		_, err := svc.Create(ctx, "u1", food())
		require.NoError(t, err)
	}
	_, err := svc.Create(ctx, "u1", food())
	assert.ErrorIs(t, err, domain.ErrLimitReached, "the defaults count")

	_, err = svc.Create(ctx, "u2", food())
	assert.NoError(t, err, "the limit is per user")
}

func TestCreateReportsStoreFailures(t *testing.T) {
	store, marker := newMemStore(), newMemMarker()
	marker.seeded["u1"] = t0
	store.err = errors.New("mongo down")
	_, err := newService(store, marker, 0).Create(context.Background(), "u1", food())
	assert.ErrorIs(t, err, store.err)
}

func TestUpdate(t *testing.T) {
	store, marker := newMemStore(), newMemMarker()
	svc := newService(store, marker, 0)
	ctx := context.Background()
	list, err := svc.List(ctx, "u1", "")
	require.NoError(t, err)
	groceries := list[1]

	later := t0.Add(time.Hour)
	svc.now = func() time.Time { return later }
	updated, err := svc.Update(ctx, "u1", groceries.ID, domain.CategoryPatch{Name: ptr("Supermarket"), Color: ptr("#00aa00")})
	require.NoError(t, err)
	assert.Equal(t, "Supermarket", updated.Name)
	assert.Equal(t, "#00AA00", updated.Color)
	assert.Equal(t, "groceries", updated.DefaultKey)
	assert.Equal(t, later.Truncate(time.Millisecond), updated.UpdatedAt)
	assert.Equal(t, updated, store.categories[groceries.ID])
	assert.Equal(t, 1, store.updates)

	same, err := svc.Update(ctx, "u1", groceries.ID, domain.CategoryPatch{Name: ptr("Supermarket")})
	require.NoError(t, err)
	assert.Equal(t, updated, same)
	assert.Equal(t, 1, store.updates, "nothing changed, nothing written")

	_, err = svc.Update(ctx, "u1", groceries.ID, domain.CategoryPatch{Icon: ptr("Not An Icon")})
	var ve *domain.ValidationError
	assert.ErrorAs(t, err, &ve)

	store.updateErr = domain.ErrNotFound // deleted between the read and the write
	_, err = svc.Update(ctx, "u1", groceries.ID, domain.CategoryPatch{Name: ptr("Gone")})
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestAnotherUsersCategoryIsNotFound(t *testing.T) {
	store, marker := newMemStore(), newMemMarker()
	svc := newService(store, marker, 0)
	ctx := context.Background()
	c, err := svc.Create(ctx, "u1", food())
	require.NoError(t, err)

	_, err = svc.Get(ctx, "u2", c.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = svc.Update(ctx, "u2", c.ID, domain.CategoryPatch{Name: ptr("Mine")})
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.ErrorIs(t, svc.Delete(ctx, "u2", c.ID, ""), domain.ErrNotFound)
	assert.ErrorIs(t, svc.Delete(ctx, "u2", c.ID, c.ID), domain.ErrNotFound)
	assert.Equal(t, "Food", store.categories[c.ID].Name, "untouched")

	require.NoError(t, svc.Delete(ctx, "u1", c.ID, ""))
	assert.ErrorIs(t, svc.Delete(ctx, "u1", c.ID, ""), domain.ErrNotFound)
}

func TestDeleteCategoryInUse(t *testing.T) {
	store, usage := newMemStore(), newMemUsage()
	svc := NewService(store, newMemMarker(), usage, Options{Now: func() time.Time { return t0 }})
	ctx := context.Background()
	food, err := svc.Create(ctx, "u1", food())
	require.NoError(t, err)
	fun, err := svc.Create(ctx, "u1", domain.CategoryInput{Name: "Fun", Kind: "expense", Icon: "party-popper", Color: "#00AA00"})
	require.NoError(t, err)
	bonus, err := svc.Create(ctx, "u1", domain.CategoryInput{Name: "Bonus", Kind: "income", Icon: "gift", Color: "#0000AA"})
	require.NoError(t, err)
	theirs, err := svc.Create(ctx, "u2", domain.CategoryInput{Name: "Theirs", Kind: "expense", Icon: "gift", Color: "#0000AA"})
	require.NoError(t, err)
	usage.byCategory[food.ID] = 3

	assert.ErrorIs(t, svc.Delete(ctx, "u1", food.ID, ""), domain.ErrCategoryInUse)
	assert.Contains(t, store.categories, food.ID)

	refused := func(reassignTo string) map[string]string {
		var ve *domain.ValidationError
		require.ErrorAs(t, svc.Delete(ctx, "u1", food.ID, reassignTo), &ve)
		out := map[string]string{}
		for _, f := range ve.Fields {
			out[f.Field] = f.Code
		}
		return out
	}
	assert.Equal(t, map[string]string{"reassignTo": domain.CodeInvalid}, refused(food.ID), "to itself")
	assert.Equal(t, map[string]string{"reassignTo": domain.CodeInvalid}, refused("ghost"))
	assert.Equal(t, map[string]string{"reassignTo": domain.CodeInvalid}, refused(theirs.ID), "another user's reads like none")
	assert.Equal(t, map[string]string{"reassignTo": domain.CodeKindMismatch}, refused(bonus.ID))
	assert.Empty(t, usage.moves, "nothing moved")
	assert.Contains(t, store.categories, food.ID)

	// A transaction created between the move and the deletion moves too.
	usage.onMove = func() { usage.byCategory[food.ID]++ }
	require.NoError(t, svc.Delete(ctx, "u1", food.ID, fun.ID))
	assert.NotContains(t, store.categories, food.ID)
	assert.Equal(t, map[string]int{fun.ID: 4}, usage.byCategory)
	at := t0.Truncate(time.Millisecond).Format(time.StampMilli)
	assert.Equal(t, []string{food.ID + "->" + fun.ID + "@" + at, food.ID + "->" + fun.ID + "@" + at}, usage.moves)

	// Moving from a category without transactions just deletes it.
	require.NoError(t, svc.Delete(ctx, "u1", bonus.ID, ""))
}

func TestDeleteCategoryFailures(t *testing.T) {
	ctx := context.Background()
	setup := func() (*Service, *memStore, *memUsage, domain.Category, domain.Category) {
		store, usage := newMemStore(), newMemUsage()
		svc := NewService(store, newMemMarker(), usage, Options{Now: func() time.Time { return t0 }})
		a, err := svc.Create(ctx, "u1", food())
		require.NoError(t, err)
		b, err := svc.Create(ctx, "u1", food())
		require.NoError(t, err)
		return svc, store, usage, a, b
	}

	svc, _, usage, a, _ := setup()
	usage.err = errors.New("mongo down")
	assert.ErrorContains(t, svc.Delete(ctx, "u1", a.ID, ""), "check category use: mongo down")

	svc, store, usage, a, b := setup()
	usage.moveErr = errors.New("mongo down")
	assert.ErrorContains(t, svc.Delete(ctx, "u1", a.ID, b.ID), "move transactions: mongo down")
	assert.Contains(t, store.categories, a.ID, "nothing deleted before the move")

	svc, store, usage, a, b = setup()
	usage.onMove = func() { usage.moveErr = errors.New("mongo down") }
	assert.ErrorContains(t, svc.Delete(ctx, "u1", a.ID, b.ID), "move late transactions: mongo down")
	assert.NotContains(t, store.categories, a.ID, "already deleted: only a write racing the deletion is left behind")

	svc, store, _, a, b = setup()
	store.err = errors.New("mongo down")
	assert.ErrorIs(t, svc.Delete(ctx, "u1", a.ID, b.ID), store.err)
}

func TestNewServiceDefaults(t *testing.T) {
	svc := NewService(newMemStore(), newMemMarker(), newMemUsage(), Options{})
	assert.Equal(t, DefaultMaxCategories, svc.max)
	assert.WithinDuration(t, time.Now(), svc.now(), time.Minute)
}
