//go:build integration

package mongo_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

func newCategory(t *testing.T, userID, name, kind string, at time.Time) domain.Category {
	t.Helper()
	c, err := domain.NewCategory(userID, domain.CategoryInput{Name: name, Kind: kind, Icon: "tag", Color: "#123ABC"}, at)
	require.NoError(t, err)
	return c
}

func categoryKeys(list []domain.Category) []string {
	out := make([]string, len(list))
	for i, c := range list {
		out[i] = c.DefaultKey
		if c.Name != "" {
			out[i] = c.Name
		}
	}
	return out
}

func TestCategoriesLifecycle(t *testing.T) {
	ctx := context.Background()
	categories := repo.NewCategories(testDB(t))
	owner := bson.NewObjectID().Hex()

	defaults := domain.NewDefaultCategories(owner, now)
	require.NoError(t, categories.InsertDefaults(ctx, defaults))
	want := categoryKeys(defaults)

	list, err := categories.List(ctx, owner, "")
	require.NoError(t, err)
	assert.Equal(t, want, categoryKeys(list), "the seed order survives equal creation times")
	groceries := list[1]
	assert.Equal(t, "groceries", groceries.DefaultKey)
	assert.Empty(t, groceries.Name)

	got, err := categories.ByID(ctx, owner, groceries.ID)
	require.NoError(t, err)
	assert.Equal(t, groceries, got, "every field round-trips")

	// A category of the owner has no default key, so the partial unique
	// index never blocks a second one.
	food, err := categories.Insert(ctx, newCategory(t, owner, "Food", "expense", now.Add(time.Minute)))
	require.NoError(t, err)
	require.Len(t, food.ID, 24)
	_, err = categories.Insert(ctx, newCategory(t, owner, "Food", "expense", now.Add(2*time.Minute)))
	require.NoError(t, err, "names may repeat")
	_, err = categories.Insert(ctx, newCategory(t, owner, "Bonus", "income", now.Add(3*time.Minute)))
	require.NoError(t, err)

	income, err := categories.List(ctx, owner, domain.CategoryIncome)
	require.NoError(t, err)
	assert.Equal(t, []string{"salary", "side_income", "investment_income", "refunds", "gifts_received", "other_income", "Bonus"}, categoryKeys(income))
	n, err := categories.Count(ctx, owner)
	require.NoError(t, err)
	assert.Equal(t, len(defaults)+3, n)

	later := now.Add(time.Hour)
	groceries.Name, groceries.Icon, groceries.Color, groceries.UpdatedAt = "Supermarket", "store", "#00AA00", later
	groceries.Kind, groceries.DefaultKey = domain.CategoryIncome, "salary" // never stored by Update
	require.NoError(t, categories.Update(ctx, groceries))
	stored, err := categories.ByID(ctx, owner, groceries.ID)
	require.NoError(t, err)
	assert.Equal(t, "Supermarket", stored.Name)
	assert.Equal(t, "store", stored.Icon)
	assert.Equal(t, "#00AA00", stored.Color)
	assert.Equal(t, later, stored.UpdatedAt)
	assert.Equal(t, domain.CategoryExpense, stored.Kind, "kind is immutable")
	assert.Equal(t, "groceries", stored.DefaultKey, "the default key stays")
	assert.Equal(t, "Supermarket", stored.DisplayName(i18n.EnUS))

	require.NoError(t, categories.Delete(ctx, owner, food.ID))
	assert.ErrorIs(t, categories.Delete(ctx, owner, food.ID), domain.ErrNotFound)
	_, err = categories.ByID(ctx, owner, food.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestInsertDefaultsNeverDuplicates(t *testing.T) {
	ctx := context.Background()
	categories := repo.NewCategories(testDB(t))
	owner, other := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()

	// Concurrent first reads seed at the same time.
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Go(func() { errs[i] = categories.InsertDefaults(ctx, domain.NewDefaultCategories(owner, now)) })
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	list, err := categories.List(ctx, owner, "")
	require.NoError(t, err)
	assert.Len(t, list, len(domain.NewDefaultCategories(owner, now)))

	// A partial seed is completed, and a renamed default is kept as it is.
	require.NoError(t, categories.Delete(ctx, owner, list[0].ID))
	renamed := list[1]
	renamed.Name = "Mine"
	require.NoError(t, categories.Update(ctx, renamed))
	require.NoError(t, categories.InsertDefaults(ctx, domain.NewDefaultCategories(owner, now.Add(time.Hour))))
	again, err := categories.List(ctx, owner, "")
	require.NoError(t, err)
	assert.Len(t, again, len(list))
	byKey := map[string]domain.Category{}
	for _, c := range again {
		byKey[c.DefaultKey] = c
	}
	assert.Equal(t, "Mine", byKey[renamed.DefaultKey].Name)
	assert.Equal(t, now.Add(time.Hour), byKey[list[0].DefaultKey].CreatedAt, "the deleted one is stored again by the store; the use case stops that")

	// The unique index is per user.
	require.NoError(t, categories.InsertDefaults(ctx, domain.NewDefaultCategories(other, now)))
	n, err := categories.Count(ctx, other)
	require.NoError(t, err)
	assert.Equal(t, len(list), n)

	assert.NoError(t, categories.InsertDefaults(ctx, nil))
	assert.Error(t, categories.InsertDefaults(ctx, []domain.Category{newCategory(t, owner, "No key", "expense", now)}),
		"only defaults go through InsertDefaults")
}

func TestCategoriesAreScopedToTheirOwner(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	categories := repo.NewCategories(db)
	owner, intruder := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()

	c, err := categories.Insert(ctx, newCategory(t, owner, "Food", "expense", now))
	require.NoError(t, err)

	_, err = categories.ByID(ctx, intruder, c.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	stolen := c
	stolen.UserID, stolen.Name = intruder, "Mine now"
	assert.ErrorIs(t, categories.Update(ctx, stolen), domain.ErrNotFound)
	assert.ErrorIs(t, categories.Delete(ctx, intruder, c.ID), domain.ErrNotFound)

	list, err := categories.List(ctx, intruder, "")
	require.NoError(t, err)
	assert.Empty(t, list)
	n, err := categories.Count(ctx, intruder)
	require.NoError(t, err)
	assert.Zero(t, n)

	got, err := categories.ByID(ctx, owner, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "Food", got.Name, "untouched")

	for _, bad := range []string{"", "not-an-id", "zzzzzzzzzzzzzzzzzzzzzzzz"} {
		_, err = categories.ByID(ctx, owner, bad)
		assert.ErrorIs(t, err, domain.ErrNotFound, bad)
		_, err = categories.ByID(ctx, bad, c.ID)
		assert.ErrorIs(t, err, domain.ErrNotFound, bad)
	}
	list, err = categories.List(ctx, "not-an-id", "")
	require.NoError(t, err)
	assert.Empty(t, list)
	n, err = categories.Count(ctx, "not-an-id")
	require.NoError(t, err)
	assert.Zero(t, n)
	_, err = categories.Insert(ctx, newCategory(t, "not-an-id", "Food", "expense", now))
	assert.ErrorIs(t, err, domain.ErrNotFound)

	eraser := repo.NewUserEraser(db, repo.CollCategories)
	require.NoError(t, eraser.EraseUserData(ctx, owner))
	n, err = categories.Count(ctx, owner)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestCategoriesSeedMarker(t *testing.T) {
	ctx := context.Background()
	users := repo.NewUsers(testDB(t))
	u, err := users.Create(ctx, domain.NewUser("Ana", "ana.seed@example.com", "hash", i18n.PtBR, now))
	require.NoError(t, err)

	seeded, err := users.CategoriesSeeded(ctx, u.ID)
	require.NoError(t, err)
	assert.False(t, seeded, "a new user has no categories yet")

	require.NoError(t, users.MarkCategoriesSeeded(ctx, u.ID, now))
	require.NoError(t, users.MarkCategoriesSeeded(ctx, u.ID, now.Add(time.Hour)), "marking twice is fine")
	seeded, err = users.CategoriesSeeded(ctx, u.ID)
	require.NoError(t, err)
	assert.True(t, seeded)

	// The marker lives on the user document: later writes keep it.
	require.NoError(t, users.UpdateName(ctx, u.ID, "Ana Maria", now.Add(2*time.Hour)))
	seeded, err = users.CategoriesSeeded(ctx, u.ID)
	require.NoError(t, err)
	assert.True(t, seeded)
	got, err := users.ByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "Ana Maria", got.Name)

	for _, gone := range []string{bson.NewObjectID().Hex(), "not-an-id"} {
		seeded, err = users.CategoriesSeeded(ctx, gone)
		require.NoError(t, err)
		assert.True(t, seeded, "an account that does not exist gets nothing written")
		assert.NoError(t, users.MarkCategoriesSeeded(ctx, gone, now))
	}
}
