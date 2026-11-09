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

func TestTransactionsDashboardSums(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	store := repo.NewTransactions(db)
	a, b := newLedger(bson.NewObjectID().Hex()), newLedger(bson.NewObjectID().Hex())
	card := newCard(a.owner)
	purchase, err := domain.NewTransaction(a.owner, domain.TransactionInput{
		Kind: "expense", AccountID: card.ID, CategoryID: a.food.ID, Amount: 3000, Date: "2026-11-15",
	}, domain.TransactionRefs{Account: card, Category: a.food}, now)
	require.NoError(t, err)
	insertAll(t, store,
		a.spend(t, a.bank, a.food, 500, "2025-01-10", "before the window"),
		a.spend(t, a.bank, a.food, 1000, "2026-10-31", ""),
		a.spend(t, a.bank, a.food, 2000, "2026-11-01", ""),
		purchase,
		a.earn(t, a.bank, 50000, "2026-11-05"),
		// Between currencies: 10.000 BRL out, 2.000 USD in.
		a.move(t, a.bank, a.dollars, 10000, ptr(int64(2000)), "2026-11-06"),
		a.spend(t, a.bank, a.food, 700, "2026-12-01", "after it"),
		b.spend(t, b.bank, b.food, 9999, "2026-11-10", "another user's"),
	)
	nov, oct := domain.NewMonth(2026, time.November), domain.NewMonth(2026, time.October)

	flows, err := store.Flows(ctx, a.owner, mustDay(t, "2026-11-01"), mustDay(t, "2026-11-30"))
	require.NoError(t, err)
	assert.ElementsMatch(t, []domain.FlowSum{
		{Month: nov, Currency: "BRL", Kind: domain.TransactionExpense, CategoryID: a.food.ID, Amount: 5000, Count: 2},
		{Month: nov, Currency: "BRL", Kind: domain.TransactionIncome, CategoryID: a.salary.ID, Amount: 50000, Count: 1},
	}, flows, "the card purchase on its day, no transfer")
	flows, err = store.Flows(ctx, a.owner, mustDay(t, "2026-10-01"), mustDay(t, "2026-11-30"))
	require.NoError(t, err)
	assert.Contains(t, flows, domain.FlowSum{Month: oct, Currency: "BRL", Kind: domain.TransactionExpense, CategoryID: a.food.ID, Amount: 1000, Count: 1})
	assert.Len(t, flows, 3)

	legs, err := store.LegSums(ctx, a.owner, oct, mustDay(t, "2026-11-30"))
	require.NoError(t, err)
	assert.ElementsMatch(t, []domain.LegSum{
		{AccountID: a.bank.ID, Sum: -500},
		{AccountID: a.bank.ID, Month: oct, Sum: -1000},
		{AccountID: a.bank.ID, Month: nov, Sum: -2000 + 50000 - 10000},
		{AccountID: card.ID, Month: nov, Sum: -3000},
		{AccountID: a.dollars.ID, Month: nov, Sum: 2000},
	}, legs, "December is after the cut")
	legs, err = store.LegSums(ctx, a.owner, nov, mustDay(t, "2026-11-05"))
	require.NoError(t, err)
	assert.ElementsMatch(t, []domain.LegSum{
		{AccountID: a.bank.ID, Sum: -1500},
		{AccountID: a.bank.ID, Month: nov, Sum: 48000},
	}, legs)

	// Another user's never count; an id that cannot exist owns nothing.
	flows, err = store.Flows(ctx, b.owner, mustDay(t, "2026-11-01"), mustDay(t, "2026-11-30"))
	require.NoError(t, err)
	assert.Equal(t, []domain.FlowSum{{Month: nov, Currency: "BRL", Kind: domain.TransactionExpense, CategoryID: b.food.ID, Amount: 9999, Count: 1}}, flows)
	legs, err = store.LegSums(ctx, b.owner, oct, mustDay(t, "2026-11-30"))
	require.NoError(t, err)
	assert.Equal(t, []domain.LegSum{{AccountID: b.bank.ID, Month: nov, Sum: -9999}}, legs)
	flows, err = store.Flows(ctx, "not-an-id", mustDay(t, "2026-11-01"), mustDay(t, "2026-11-30"))
	require.NoError(t, err)
	assert.Empty(t, flows)
	legs, err = store.LegSums(ctx, "not-an-id", oct, mustDay(t, "2026-11-30"))
	require.NoError(t, err)
	assert.Empty(t, legs)
}

func TestDashboardQueriesReadOnlyTheirEntries(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	store := repo.NewTransactions(db)
	l, other := newLedger(bson.NewObjectID().Hex()), newLedger(bson.NewObjectID().Hex())
	// Two years of another user, and a year of the owner's before the
	// window and after the cut: the sums read the owner's days only.
	for i := range 24 {
		day := domain.NewMonth(2025, time.January).AddMonths(i).Day(10).String()
		insertAll(t, store, other.spend(t, other.bank, other.food, 100, day, ""), other.spend(t, other.bank, other.food, 100, day, ""))
	}
	for i := range 12 {
		day := domain.NewMonth(2025, time.January).AddMonths(i).Day(10).String()
		insertAll(t, store, l.spend(t, l.bank, l.food, 100, day, ""))
	}
	for range 5 {
		insertAll(t, store, l.spend(t, l.bank, l.food, 100, "2026-11-03", ""), l.move(t, l.bank, l.wallet, 100, nil, "2026-11-04"))
	}
	insertAll(t, store, l.spend(t, l.bank, l.food, 100, "2026-12-03", "after the cut"))

	op := profile(t, db, func() {
		flows, err := store.Flows(ctx, l.owner, mustDay(t, "2026-11-01"), mustDay(t, "2026-11-30"))
		require.NoError(t, err)
		assert.Len(t, flows, 1)
	})
	assert.LessOrEqual(t, op.KeysExamined, 11, "the month's ten entries: %s", op.PlanSummary)
	assert.Contains(t, op.PlanSummary, "IXSCAN")

	op = profile(t, db, func() {
		legs, err := store.LegSums(ctx, l.owner, domain.NewMonth(2025, time.December), mustDay(t, "2026-11-30"))
		require.NoError(t, err)
		assert.NotEmpty(t, legs)
	})
	assert.LessOrEqual(t, op.KeysExamined, 23, "the owner's 22 entries up to the cut: %s", op.PlanSummary)
	assert.Contains(t, op.PlanSummary, "IXSCAN")
}
