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

func newInvestment(t *testing.T, userID, name string, typ domain.InvestmentType, accountID string) domain.Investment {
	t.Helper()
	var refs domain.InvestmentRefs
	if accountID != "" {
		refs.Account = &domain.Account{ID: accountID, UserID: userID, Type: domain.AccountBrokerage, Currency: "BRL"}
	}
	inv, err := domain.NewInvestment(userID, domain.InvestmentInput{
		Name: name, Type: string(typ), Currency: "BRL", AccountID: accountID,
	}, refs, now)
	require.NoError(t, err)
	return inv
}

func TestInvestmentsLifecycle(t *testing.T) {
	ctx := context.Background()
	store := repo.NewInvestments(testDB(t))
	owner, other := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	broker := bson.NewObjectID().Hex()

	stock, err := store.Insert(ctx, newInvestment(t, owner, "Stock", domain.InvestmentStock, broker))
	require.NoError(t, err)
	require.Len(t, stock.ID, 24)
	cdb, err := store.Insert(ctx, newInvestment(t, owner, "CDB", domain.InvestmentFixedIncome, broker))
	require.NoError(t, err)
	foreign, err := store.Insert(ctx, newInvestment(t, other, "Theirs", domain.InvestmentCrypto, ""))
	require.NoError(t, err)

	got, err := store.ByID(ctx, owner, stock.ID)
	require.NoError(t, err)
	assert.Equal(t, stock, got, "every field round-trips")
	assert.Equal(t, broker, got.AccountID)
	assert.Equal(t, domain.ModeUnits, got.Mode)

	list, err := store.List(ctx, owner)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, []string{stock.ID, cdb.ID}, []string{list[0].ID, list[1].ID}, "oldest first")
	n, err := store.Count(ctx, owner)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// Another user's investment is not found, for every method.
	_, err = store.ByID(ctx, owner, foreign.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	foreign.UserID = owner
	assert.ErrorIs(t, store.Update(ctx, foreign), domain.ErrNotFound)
	assert.ErrorIs(t, store.Delete(ctx, owner, foreign.ID), domain.ErrNotFound)
	_, err = store.ByID(ctx, owner, "not-an-id")
	assert.ErrorIs(t, err, domain.ErrNotFound)

	// Update sets and clears the ticker and the account; mode and currency
	// stay.
	at := now.Add(time.Hour)
	stock.Name, stock.Type, stock.Ticker, stock.AccountID, stock.UpdatedAt = "Renamed", domain.InvestmentETF, "ABC11", "", at
	stock.Mode, stock.Currency = domain.ModeValue, "USD"
	require.NoError(t, store.Update(ctx, stock))
	got, err = store.ByID(ctx, owner, stock.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.Name)
	assert.Equal(t, domain.InvestmentETF, got.Type)
	assert.Equal(t, "ABC11", got.Ticker)
	assert.Empty(t, got.AccountID)
	assert.Equal(t, domain.ModeUnits, got.Mode)
	assert.Equal(t, "BRL", got.Currency)
	assert.Equal(t, at, got.UpdatedAt)
	stock.Ticker = ""
	require.NoError(t, store.Update(ctx, stock))
	got, err = store.ByID(ctx, owner, stock.ID)
	require.NoError(t, err)
	assert.Empty(t, got.Ticker)

	// Unlinking an account touches only its investments of its owner.
	theirs, err := store.Insert(ctx, newInvestment(t, other, "Same account id", domain.InvestmentStock, broker))
	require.NoError(t, err)
	require.NoError(t, store.UnlinkAccount(ctx, owner, broker, at))
	got, err = store.ByID(ctx, owner, cdb.ID)
	require.NoError(t, err)
	assert.Empty(t, got.AccountID)
	assert.Equal(t, at, got.UpdatedAt)
	kept, err := store.ByID(ctx, other, theirs.ID)
	require.NoError(t, err)
	assert.Equal(t, broker, kept.AccountID, "another user's investment keeps its account")
	require.NoError(t, store.UnlinkAccount(ctx, "bad", "bad", at))

	require.NoError(t, store.Delete(ctx, owner, stock.ID))
	assert.ErrorIs(t, store.Delete(ctx, owner, stock.ID), domain.ErrNotFound)
	list, err = store.List(ctx, "not-an-id")
	require.NoError(t, err)
	assert.Empty(t, list)
}

// movementOf returns a movement of inv built through the domain.
func movementOf(t *testing.T, inv domain.Investment, in domain.MovementInput) domain.Movement {
	t.Helper()
	var refs domain.MovementRefs
	if in.CategoryID != "" {
		refs.Category = &domain.Category{ID: in.CategoryID, UserID: inv.UserID, Kind: domain.CategoryIncome}
	}
	m, err := domain.NewMovement(inv, in, refs, domain.DateOf(now), now)
	require.NoError(t, err)
	return m
}

func amountOf(v int64) *int64 { return &v }

func TestInvestmentMovements(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	invs, store := repo.NewInvestments(db), repo.NewInvestmentMovements(db)
	owner, other := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	dividends, rent := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	crypto, err := invs.Insert(ctx, newInvestment(t, owner, "Coin", domain.InvestmentCrypto, ""))
	require.NoError(t, err)
	cdb, err := invs.Insert(ctx, newInvestment(t, owner, "CDB", domain.InvestmentFixedIncome, ""))
	require.NoError(t, err)
	theirs, err := invs.Insert(ctx, newInvestment(t, other, "Theirs", domain.InvestmentStock, ""))
	require.NoError(t, err)

	buy, err := store.Insert(ctx, movementOf(t, crypto, domain.MovementInput{
		Type: "buy", Date: "2026-10-01", Quantity: "0.01234567", Amount: amountOf(432098), Fees: amountOf(150),
	}))
	require.NoError(t, err)
	quote, err := store.Insert(ctx, movementOf(t, crypto, domain.MovementInput{Type: "valuation", Date: "2026-11-01", Price: "350000.12345678"}))
	require.NoError(t, err)
	income, err := store.Insert(ctx, movementOf(t, cdb, domain.MovementInput{Type: "income", Date: "2026-10-15", Amount: amountOf(1000), CategoryID: dividends}))
	require.NoError(t, err)
	value, err := store.Insert(ctx, movementOf(t, cdb, domain.MovementInput{Type: "valuation", Date: "2026-10-31", Amount: amountOf(0)}))
	require.NoError(t, err)
	foreign, err := store.Insert(ctx, movementOf(t, theirs, domain.MovementInput{Type: "income", Date: "2026-10-15", Amount: amountOf(500), CategoryID: dividends}))
	require.NoError(t, err)

	// Every field round-trips, the Decimal128 ones exactly.
	got, err := store.ByID(ctx, owner, crypto.ID, buy.ID)
	require.NoError(t, err)
	assert.Equal(t, buy, got)
	assert.Equal(t, "0.01234567", got.Quantity.String())
	got, err = store.ByID(ctx, owner, crypto.ID, quote.ID)
	require.NoError(t, err)
	assert.Equal(t, "350000.12345678", got.Price.String())
	assert.Equal(t, quote, got)
	got, err = store.ByID(ctx, owner, cdb.ID, value.ID)
	require.NoError(t, err)
	assert.Equal(t, value, got, "a valuation worth nothing keeps its zero amount")

	// Only the fields of the shape are stored.
	var raw bson.M
	require.NoError(t, db.Collection(repo.CollInvestmentMovements).FindOne(ctx, bson.D{{Key: "_id", Value: mustOID(t, quote.ID)}}).Decode(&raw))
	assert.NotContains(t, raw, "amount")
	assert.NotContains(t, raw, "quantity")
	assert.IsType(t, bson.Decimal128{}, raw["price"])
	require.NoError(t, db.Collection(repo.CollInvestmentMovements).FindOne(ctx, bson.D{{Key: "_id", Value: mustOID(t, buy.ID)}}).Decode(&raw))
	assert.IsType(t, bson.Decimal128{}, raw["quantity"])
	assert.Equal(t, "0.01234567", raw["quantity"].(bson.Decimal128).String())

	// A movement is reached through its own investment and owner only.
	_, err = store.ByID(ctx, owner, cdb.ID, buy.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound, "through another investment")
	_, err = store.ByID(ctx, owner, theirs.ID, foreign.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound, "another user's")
	assert.ErrorIs(t, store.Delete(ctx, owner, theirs.ID, foreign.ID), domain.ErrNotFound)
	foreign.UserID = owner
	assert.ErrorIs(t, store.Update(ctx, foreign), domain.ErrNotFound)

	of, err := store.Of(ctx, owner, crypto.ID)
	require.NoError(t, err)
	assert.Equal(t, []domain.Movement{quote, buy}, of, "newest first")
	all, err := store.All(ctx, owner)
	require.NoError(t, err)
	assert.Len(t, all, 2)
	assert.Len(t, all[crypto.ID], 2)
	assert.Len(t, all[cdb.ID], 2)
	n, err := store.Count(ctx, owner, cdb.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// Update stores the day and the shape's fields.
	at := now.Add(time.Hour)
	income.Date, income.Amount, income.CategoryID, income.UpdatedAt = mustDay(t, "2026-10-16"), 1200, rent, at
	require.NoError(t, store.Update(ctx, income))
	got, err = store.ByID(ctx, owner, cdb.ID, income.ID)
	require.NoError(t, err)
	assert.Equal(t, income, got)

	// The dashboard's income: by month, currency and category, theirs left
	// out.
	_, err = store.Insert(ctx, movementOf(t, crypto, domain.MovementInput{Type: "income", Date: "2026-10-20", Amount: amountOf(300), CategoryID: rent}))
	require.NoError(t, err)
	_, err = store.Insert(ctx, movementOf(t, crypto, domain.MovementInput{Type: "income", Date: "2026-09-30", Amount: amountOf(50), CategoryID: rent}))
	require.NoError(t, err)
	_, err = store.Insert(ctx, movementOf(t, crypto, domain.MovementInput{Type: "fee", Date: "2026-10-20", Amount: amountOf(99)}))
	require.NoError(t, err)
	flows, err := store.IncomeFlows(ctx, owner, mustDay(t, "2026-10-01"), mustDay(t, "2026-10-31"))
	require.NoError(t, err)
	assert.Equal(t, []domain.FlowSum{{
		Month: mustMonth(t, "2026-10"), Currency: "BRL", Kind: domain.TransactionIncome, CategoryID: rent, Amount: 1500, Count: 2,
	}}, flows)

	// Category use and reassignment.
	inUse, err := store.CategoryInUse(ctx, owner, dividends)
	require.NoError(t, err)
	assert.False(t, inUse, "the income moved to rent")
	inUse, err = store.CategoryInUse(ctx, other, dividends)
	require.NoError(t, err)
	assert.True(t, inUse)
	require.NoError(t, store.ReassignCategory(ctx, owner, rent, dividends, at))
	inUse, err = store.CategoryInUse(ctx, owner, rent)
	require.NoError(t, err)
	assert.False(t, inUse)
	inUse, err = store.CategoryInUse(ctx, owner, dividends)
	require.NoError(t, err)
	assert.True(t, inUse)

	require.NoError(t, store.Delete(ctx, owner, crypto.ID, buy.ID))
	assert.ErrorIs(t, store.Delete(ctx, owner, crypto.ID, buy.ID), domain.ErrNotFound)
	require.NoError(t, store.DeleteOf(ctx, owner, crypto.ID))
	of, err = store.Of(ctx, owner, crypto.ID)
	require.NoError(t, err)
	assert.Empty(t, of)
	n, err = store.Count(ctx, other, theirs.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "another user's movements stay")
}

func TestInvestmentMovementsPagesAndIndexes(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	invs, store := repo.NewInvestments(db), repo.NewInvestmentMovements(db)
	owner := bson.NewObjectID().Hex()
	category := bson.NewObjectID().Hex()
	stock, err := invs.Insert(ctx, newInvestment(t, owner, "Stock", domain.InvestmentStock, ""))
	require.NoError(t, err)
	fund, err := invs.Insert(ctx, newInvestment(t, owner, "Fund", domain.InvestmentRealEstateFund, ""))
	require.NoError(t, err)
	// 36 months of a fund paying every month, and a stock bought twice a
	// month on the same day.
	for i := range 36 {
		date := mustMonth(t, "2023-11").AddMonths(i).Day(5).String()
		_, err := store.Insert(ctx, movementOf(t, fund, domain.MovementInput{Type: "income", Date: date, Amount: amountOf(100), CategoryID: category}))
		require.NoError(t, err)
		for range 2 {
			_, err := store.Insert(ctx, movementOf(t, stock, domain.MovementInput{Type: "buy", Date: date, Quantity: "1", Amount: amountOf(1000)}))
			require.NoError(t, err)
		}
	}

	var pages [][]domain.Movement
	var after *domain.MovementCursor
	for {
		page, err := store.Page(ctx, owner, stock.ID, domain.MovementFilter{}, after, 25)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		pages = append(pages, page)
		last := page[len(page)-1]
		after = &domain.MovementCursor{Date: last.Date, ID: last.ID}
	}
	require.Len(t, pages, 3)
	seen := map[string]bool{}
	var flat []domain.Movement
	for _, p := range pages {
		for _, m := range p {
			assert.False(t, seen[m.ID], "no movement twice")
			seen[m.ID] = true
			flat = append(flat, m)
		}
	}
	require.Len(t, flat, 72, "a cursor between two movements of one day loses none")
	for i := 1; i < len(flat); i++ {
		prev, cur := flat[i-1], flat[i]
		assert.True(t, cur.Date.Before(prev.Date) || (cur.Date.Equal(prev.Date) && cur.ID < prev.ID), "newest first")
	}

	page, err := store.Page(ctx, owner, fund.ID, domain.MovementFilter{Type: domain.MovementBuy}, nil, 10)
	require.NoError(t, err)
	assert.Empty(t, page, "filtered by type")
	page, err = store.Page(ctx, owner, stock.ID, domain.MovementFilter{}, &domain.MovementCursor{Date: mustDay(t, "2026-01-01"), ID: "bad"}, 10)
	require.NoError(t, err)
	assert.Empty(t, page, "a cursor that cannot exist")

	op := profileOn(t, db, repo.CollInvestmentMovements, func() {
		flows, err := store.IncomeFlows(ctx, owner, mustDay(t, "2026-01-01"), mustDay(t, "2026-03-31"))
		require.NoError(t, err)
		require.Len(t, flows, 3)
	})
	assert.LessOrEqual(t, op.KeysExamined, 4, "the income of three months reads three entries: %s", op.PlanSummary)
	assert.Contains(t, op.PlanSummary, "IXSCAN { user_id: 1, date: 1 }", "the partial index of the income")

	op = profileOn(t, db, repo.CollInvestmentMovements, func() {
		page, err := store.Page(ctx, owner, stock.ID, domain.MovementFilter{}, nil, 10)
		require.NoError(t, err)
		require.Len(t, page, 10)
	})
	assert.LessOrEqual(t, op.KeysExamined, 10, "a page reads its own entries: %s", op.PlanSummary)
	assert.NotContains(t, op.PlanSummary, "SORT {", "the index gives the order")
}

func mustOID(t *testing.T, id string) bson.ObjectID {
	t.Helper()
	oid, err := bson.ObjectIDFromHex(id)
	require.NoError(t, err)
	return oid
}
