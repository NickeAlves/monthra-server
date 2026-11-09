//go:build integration

package mongo_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

var budgetsMonth = domain.NewMonth(2026, time.November)

func newBudget(t *testing.T, userID, categoryID string, limit int64) domain.Budget {
	t.Helper()
	b, err := domain.NewBudget(userID, domain.BudgetInput{CategoryID: categoryID, Currency: "BRL", Limit: limit}, budgetsMonth, now)
	require.NoError(t, err)
	return b
}

func budgetIDs(list []domain.Budget) []string {
	out := make([]string, len(list))
	for i, b := range list {
		out[i] = b.ID
	}
	return out
}

func TestBudgetsLifecycle(t *testing.T) {
	ctx := context.Background()
	budgets := repo.NewBudgets(testDB(t))
	owner, other := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	food, fun := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()

	b1, err := budgets.Insert(ctx, newBudget(t, owner, food, 100000))
	require.NoError(t, err)
	require.Len(t, b1.ID, 24)
	_, err = budgets.Insert(ctx, newBudget(t, owner, food, 5000))
	assert.ErrorIs(t, err, domain.ErrBudgetExists, "one budget per category and month")
	b2, err := budgets.Insert(ctx, newBudget(t, owner, fun, 20000))
	require.NoError(t, err)
	_, err = budgets.Insert(ctx, newBudget(t, other, bson.NewObjectID().Hex(), 1000))
	require.NoError(t, err)

	got, err := budgets.ByID(ctx, owner, b1.ID)
	require.NoError(t, err)
	assert.Equal(t, b1, got, "every field round-trips")
	assert.Equal(t, budgetsMonth, got.Month)

	list, err := budgets.List(ctx, owner, budgetsMonth)
	require.NoError(t, err)
	assert.Equal(t, []string{b1.ID, b2.ID}, budgetIDs(list), "in creation order")
	list, err = budgets.List(ctx, owner, budgetsMonth.AddMonths(-1))
	require.NoError(t, err)
	assert.Empty(t, list)

	// Update stores the limit, the share and the marks, setting and
	// clearing them; the category, month and currency never change.
	at := now.Add(time.Hour)
	b1.Limit, b1.WarnAt, b1.UpdatedAt, b1.WarnedAt, b1.ExceededAt = 200000, 90, at, &at, &at
	b1.CategoryID, b1.Currency, b1.Month = fun, "USD", budgetsMonth.AddMonths(-3)
	require.NoError(t, budgets.Update(ctx, b1))
	stored, err := budgets.ByID(ctx, owner, b1.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(200000), stored.Limit)
	assert.Equal(t, 90, stored.WarnAt)
	assert.Equal(t, at, stored.UpdatedAt)
	require.NotNil(t, stored.WarnedAt)
	assert.Equal(t, at, *stored.WarnedAt)
	assert.Equal(t, food, stored.CategoryID)
	assert.Equal(t, "BRL", stored.Currency)
	assert.Equal(t, budgetsMonth, stored.Month)

	stored.WarnedAt, stored.ExceededAt = nil, nil
	require.NoError(t, budgets.Update(ctx, stored))
	cleared, err := budgets.ByID(ctx, owner, b1.ID)
	require.NoError(t, err)
	assert.Nil(t, cleared.WarnedAt)
	assert.Nil(t, cleared.ExceededAt)

	// IDOR: another user's id is not found, for every operation.
	for _, uid := range []string{other, "not-an-id"} {
		_, err = budgets.ByID(ctx, uid, b1.ID)
		assert.ErrorIs(t, err, domain.ErrNotFound)
		foreign := stored
		foreign.UserID = uid
		assert.ErrorIs(t, budgets.Update(ctx, foreign), domain.ErrNotFound)
		assert.ErrorIs(t, budgets.Delete(ctx, uid, b1.ID), domain.ErrNotFound)
		claimed, err := budgets.ClaimAlert(ctx, uid, b1.ID, domain.BudgetWarning, at)
		require.NoError(t, err)
		assert.False(t, claimed)
		require.NoError(t, budgets.DeleteByCategory(ctx, uid, food))
	}
	_, err = budgets.ByID(ctx, owner, "not-an-id")
	assert.ErrorIs(t, err, domain.ErrNotFound)
	list, err = budgets.List(ctx, "not-an-id", budgetsMonth)
	require.NoError(t, err)
	assert.Empty(t, list)

	require.NoError(t, budgets.Delete(ctx, owner, b2.ID))
	assert.ErrorIs(t, budgets.Delete(ctx, owner, b2.ID), domain.ErrNotFound)
	list, err = budgets.List(ctx, owner, budgetsMonth)
	require.NoError(t, err)
	assert.Equal(t, []string{b1.ID}, budgetIDs(list))

	// The other user's budget survived everything the owner did.
	list, err = budgets.List(ctx, other, budgetsMonth)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestBudgetsRolledAndDeletedByCategory(t *testing.T) {
	ctx := context.Background()
	budgets := repo.NewBudgets(testDB(t))
	owner, other := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	food, fun := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()

	november := []domain.Budget{newBudget(t, owner, food, 1000), newBudget(t, owner, fun, 2000)}
	require.NoError(t, budgets.InsertRolled(ctx, november))
	require.NoError(t, budgets.InsertRolled(ctx, november), "a repeated rollover skips what is there")
	require.NoError(t, budgets.InsertRolled(ctx, nil))
	list, err := budgets.List(ctx, owner, budgetsMonth)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, food, list[0].CategoryID, "in the order of the month before")

	// A category with its own budget in the month keeps it.
	december := budgetsMonth.AddMonths(1)
	own := newBudget(t, owner, food, 9999)
	own.Month = december
	_, err = budgets.Insert(ctx, own)
	require.NoError(t, err)
	require.NoError(t, budgets.InsertRolled(ctx, domain.RollBudgets(list, december, now)))
	list, err = budgets.List(ctx, owner, december)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, int64(9999), list[0].Limit)
	assert.Equal(t, int64(2000), list[1].Limit)

	_, err = budgets.Insert(ctx, newBudget(t, other, food, 1000))
	require.NoError(t, err)
	require.NoError(t, budgets.DeleteByCategory(ctx, owner, food))
	require.NoError(t, budgets.DeleteByCategory(ctx, owner, food), "deleting again is fine")
	for _, m := range []domain.Month{budgetsMonth, december} {
		list, err = budgets.List(ctx, owner, m)
		require.NoError(t, err)
		require.Len(t, list, 1, "every month loses the category")
		assert.Equal(t, fun, list[0].CategoryID)
	}
	list, err = budgets.List(ctx, other, budgetsMonth)
	require.NoError(t, err)
	assert.Len(t, list, 1, "another user's budget of the same id stays")
}

func TestBudgetsClaimAlertOnce(t *testing.T) {
	ctx := context.Background()
	budgets := repo.NewBudgets(testDB(t))
	owner := bson.NewObjectID().Hex()
	b, err := budgets.Insert(ctx, newBudget(t, owner, bson.NewObjectID().Hex(), 1000))
	require.NoError(t, err)

	pending, err := budgets.Pending(ctx, owner, budgetsMonth)
	require.NoError(t, err)
	assert.Equal(t, []string{b.ID}, budgetIDs(pending))

	// Concurrent checks claim the warning once.
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			ok, err := budgets.ClaimAlert(ctx, owner, b.ID, domain.BudgetWarning, now)
			assert.NoError(t, err)
			if ok {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load())
	pending, err = budgets.Pending(ctx, owner, budgetsMonth)
	require.NoError(t, err)
	assert.Len(t, pending, 1, "warned but not exceeded: still pending")

	later := now.Add(time.Hour)
	ok, err := budgets.ClaimAlert(ctx, owner, b.ID, domain.BudgetExceeded, later)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = budgets.ClaimAlert(ctx, owner, b.ID, domain.BudgetExceeded, later)
	require.NoError(t, err)
	assert.False(t, ok, "once")
	stored, err := budgets.ByID(ctx, owner, b.ID)
	require.NoError(t, err)
	assert.Equal(t, now, *stored.WarnedAt, "the earlier warning stays")
	assert.Equal(t, later, *stored.ExceededAt)
	pending, err = budgets.Pending(ctx, owner, budgetsMonth)
	require.NoError(t, err)
	assert.Empty(t, pending)

	// Exceeding first marks the warning too.
	b2, err := budgets.Insert(ctx, newBudget(t, owner, bson.NewObjectID().Hex(), 1000))
	require.NoError(t, err)
	ok, err = budgets.ClaimAlert(ctx, owner, b2.ID, domain.BudgetExceeded, later)
	require.NoError(t, err)
	assert.True(t, ok)
	stored, err = budgets.ByID(ctx, owner, b2.ID)
	require.NoError(t, err)
	assert.Equal(t, later, *stored.WarnedAt)
	ok, err = budgets.ClaimAlert(ctx, owner, b2.ID, domain.BudgetWarning, later)
	require.NoError(t, err)
	assert.False(t, ok)

	_, err = budgets.ClaimAlert(ctx, owner, b2.ID, domain.BudgetOK, later)
	assert.Error(t, err, "ok is no alert")
}

func TestBudgetsRolloverMarker(t *testing.T) {
	ctx := context.Background()
	users := repo.NewUsers(testDB(t))
	u, err := users.Create(ctx, domain.NewUser("Ana", "ana.budgets@example.com", "hash", i18n.PtBR, now))
	require.NoError(t, err)

	m, err := users.BudgetsRolledThrough(ctx, u.ID)
	require.NoError(t, err)
	assert.True(t, m.IsZero(), "never rolled")

	require.NoError(t, users.MarkBudgetsRolled(ctx, u.ID, budgetsMonth))
	require.NoError(t, users.MarkBudgetsRolled(ctx, u.ID, budgetsMonth.AddMonths(-2)), "the mark never moves back")
	m, err = users.BudgetsRolledThrough(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, budgetsMonth, m)

	// Across a year: "2027-01" sorts after "2026-12".
	require.NoError(t, users.MarkBudgetsRolled(ctx, u.ID, domain.NewMonth(2027, time.January)))
	require.NoError(t, users.MarkBudgetsRolled(ctx, u.ID, domain.NewMonth(2026, time.December)))
	m, err = users.BudgetsRolledThrough(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.NewMonth(2027, time.January), m)

	for _, gone := range []string{bson.NewObjectID().Hex(), "not-an-id"} {
		m, err = users.BudgetsRolledThrough(ctx, gone)
		require.NoError(t, err)
		assert.Equal(t, domain.MaxMonth, m, "a user that is gone counts as done")
		require.NoError(t, users.MarkBudgetsRolled(ctx, gone, budgetsMonth))
	}
}
