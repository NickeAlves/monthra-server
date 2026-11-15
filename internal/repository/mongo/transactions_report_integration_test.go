//go:build integration

package mongo_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/NickeAlves/monthra-server/internal/domain"
	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

func TestTransactionsReportSums(t *testing.T) {
	ctx := context.Background()
	store := repo.NewTransactions(testDB(t))
	a, b := newLedger(bson.NewObjectID().Hex()), newLedger(bson.NewObjectID().Hex())
	insertAll(t, store,
		a.spend(t, a.bank, a.food, 500, "2026-09-30", "before the range", "trip"),
		a.spend(t, a.bank, a.food, 1000, "2026-10-01", "", "trip", "beach"),
		a.spend(t, a.wallet, a.food, 2000, "2026-10-15", "", "trip"),
		a.spend(t, a.wallet, a.otherCat, 300, "2026-11-30", "untagged"),
		a.earn(t, a.dollars, 7000, "2026-11-05"),
		a.move(t, a.bank, a.wallet, 99999, nil, "2026-10-20"),
		a.spend(t, a.bank, a.food, 800, "2026-12-01", "after the range", "trip"),
		b.spend(t, b.bank, b.food, 9999, "2026-10-10", "another user's", "trip"),
	)
	from, to := mustDay(t, "2026-10-01"), mustDay(t, "2026-11-30")

	byAccount, err := store.AccountFlows(ctx, a.owner, from, to)
	require.NoError(t, err)
	assert.ElementsMatch(t, []domain.AccountFlowSum{
		{Currency: "BRL", Kind: domain.TransactionExpense, AccountID: a.bank.ID, Amount: 1000, Count: 1},
		{Currency: "BRL", Kind: domain.TransactionExpense, AccountID: a.wallet.ID, Amount: 2300, Count: 2},
		{Currency: "USD", Kind: domain.TransactionIncome, AccountID: a.dollars.ID, Amount: 7000, Count: 1},
	}, byAccount, "no transfer, both days of the range included")

	byTag, err := store.TagFlows(ctx, a.owner, from, to)
	require.NoError(t, err)
	assert.ElementsMatch(t, []domain.TagFlowSum{
		{Currency: "BRL", Kind: domain.TransactionExpense, Tag: "trip", Amount: 3000, Count: 2},
		{Currency: "BRL", Kind: domain.TransactionExpense, Tag: "beach", Amount: 1000, Count: 1},
	}, byTag, "a transaction counts under each of its tags; untagged ones under none")

	// Another user's never count; an id that cannot exist owns nothing.
	byTag, err = store.TagFlows(ctx, b.owner, from, to)
	require.NoError(t, err)
	assert.Equal(t, []domain.TagFlowSum{{Currency: "BRL", Kind: domain.TransactionExpense, Tag: "trip", Amount: 9999, Count: 1}}, byTag)
	byAccount, err = store.AccountFlows(ctx, "not-an-id", from, to)
	require.NoError(t, err)
	assert.Empty(t, byAccount)
	byTag, err = store.TagFlows(ctx, "not-an-id", from, to)
	require.NoError(t, err)
	assert.Empty(t, byTag)
}

func TestReportQueriesReadOnlyTheirEntries(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	store := repo.NewTransactions(db)
	l, other := newLedger(bson.NewObjectID().Hex()), newLedger(bson.NewObjectID().Hex())
	// Two years of another user's and a year of the owner's outside the
	// range: the sums read the owner's days only.
	for i := range 24 {
		day := domain.NewMonth(2025, time.January).AddMonths(i).Day(10).String()
		insertAll(t, store, other.spend(t, other.bank, other.food, 100, day, "", "home"))
	}
	for i := range 12 {
		day := domain.NewMonth(2025, time.January).AddMonths(i).Day(10).String()
		insertAll(t, store, l.spend(t, l.bank, l.food, 100, day, "", "home"))
	}
	for range 5 {
		insertAll(t, store, l.spend(t, l.bank, l.food, 100, "2026-11-03", "", "home"), l.move(t, l.bank, l.wallet, 100, nil, "2026-11-04"))
	}
	from, to := mustDay(t, "2026-11-01"), mustDay(t, "2026-11-30")

	op := profile(t, db, func() {
		sums, err := store.AccountFlows(ctx, l.owner, from, to)
		require.NoError(t, err)
		assert.Len(t, sums, 1)
	})
	assert.LessOrEqual(t, op.KeysExamined, 11, "the month's ten entries: %s", op.PlanSummary)
	assert.Contains(t, op.PlanSummary, "IXSCAN")

	op = profile(t, db, func() {
		sums, err := store.TagFlows(ctx, l.owner, from, to)
		require.NoError(t, err)
		assert.Equal(t, []domain.TagFlowSum{{Currency: "BRL", Kind: domain.TransactionExpense, Tag: "home", Amount: 500, Count: 5}}, sums)
	})
	assert.LessOrEqual(t, op.KeysExamined, 11, "the month's ten entries: %s", op.PlanSummary)
	assert.Contains(t, op.PlanSummary, "IXSCAN")
}
