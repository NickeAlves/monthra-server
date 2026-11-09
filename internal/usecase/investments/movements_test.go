package investments

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

func TestCreateMovement(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	stock := f.invest(t, domain.InvestmentStock, "")

	buy := f.move(t, stock, domain.MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "10", Amount: amount(10000)})
	assert.NotEmpty(t, buy.ID)
	assert.Equal(t, stock.ID, buy.InvestmentID)
	assert.Equal(t, t0, buy.CreatedAt)
	assert.Zero(t, f.hooks.count(), "a buy changes no number of the dashboard")
	assert.Equal(t, []string{"buy"}, f.metrics.created, "counted by type")

	income := f.move(t, stock, domain.MovementInput{Type: "income", Date: "2026-11-05", Amount: amount(150), CategoryID: "dividends"})
	assert.Equal(t, "dividends", income.CategoryID)
	assert.Equal(t, []string{"u1"}, f.hooks.calls, "an income counts on the dashboard")

	// The reader's today bounds the day: within a day of UTC.
	_, err := f.svc.CreateMovement(ctx, "u1", stock.ID, domain.MovementInput{Type: "fee", Date: "2026-11-10", Amount: amount(1)}, day(t, "2026-11-10"))
	assert.NoError(t, err, "tomorrow in UTC is today somewhere")
	_, err = f.svc.CreateMovement(ctx, "u1", stock.ID, domain.MovementInput{Type: "fee", Date: "2026-11-11", Amount: amount(1)}, day(t, "2026-11-11"))
	assert.Equal(t, map[string]string{"date": domain.CodeOutOfRange}, fieldsOf(t, err), "never further")

	_, err = f.svc.CreateMovement(ctx, "u1", stock.ID, domain.MovementInput{Type: "sell", Date: "2026-11-02", Quantity: "10.00000001", Amount: amount(12000)}, domain.Date{})
	assert.Equal(t, map[string]string{"quantity": domain.CodeExceedsPosition}, fieldsOf(t, err), "more than held")
	_, err = f.svc.CreateMovement(ctx, "u1", stock.ID, domain.MovementInput{Type: "sell", Date: "2026-10-31", Quantity: "1", Amount: amount(1000)}, domain.Date{})
	assert.Equal(t, map[string]string{"quantity": domain.CodeExceedsPosition}, fieldsOf(t, err), "before the buy")
	big := f.invest(t, domain.InvestmentStock, "")
	f.move(t, big, domain.MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "11", Amount: amount(11000)})
	_, err = f.svc.CreateMovement(ctx, "u1", big.ID, domain.MovementInput{Type: "valuation", Date: "2026-11-03", Price: "10000000000"}, domain.Date{})
	assert.Equal(t, map[string]string{"amount": domain.CodeOutOfRange}, fieldsOf(t, err), "worth past MaxAmount")
	sold := f.move(t, stock, domain.MovementInput{Type: "sell", Date: "2026-11-02", Quantity: "10", Amount: amount(12000)})
	assert.Equal(t, domain.MovementSell, sold.Type)

	_, err = f.svc.CreateMovement(ctx, "u1", stock.ID, domain.MovementInput{Type: "income", Date: "2026-11-01", Amount: amount(1), CategoryID: "food"}, domain.Date{})
	assert.Equal(t, map[string]string{"categoryId": domain.CodeKindMismatch}, fieldsOf(t, err))
	_, err = f.svc.CreateMovement(ctx, "u1", stock.ID, domain.MovementInput{Type: "income", Date: "2026-11-01", Amount: amount(1), CategoryID: "broker"}, domain.Date{})
	assert.Equal(t, map[string]string{"categoryId": domain.CodeInvalid}, fieldsOf(t, err))

	_, err = f.svc.CreateMovement(ctx, "u2", stock.ID, domain.MovementInput{Type: "fee", Date: "2026-11-01", Amount: amount(1)}, domain.Date{})
	assert.ErrorIs(t, err, domain.ErrNotFound, "another user's investment")

	full := domain.MaxMovementsPerInvestment
	f.movements.count = &full
	_, err = f.svc.CreateMovement(ctx, "u1", stock.ID, domain.MovementInput{Type: "fee", Date: "2026-11-01", Amount: amount(1)}, domain.Date{})
	assert.ErrorIs(t, err, domain.ErrLimitReached)
}

func TestCreateMovementMendsBrokenRecords(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	stock := f.invest(t, domain.InvestmentStock, "")
	f.move(t, stock, domain.MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "1", Amount: amount(1000)})
	// Two sales at once got past the check.
	for range 2 {
		_, err := f.movements.Insert(ctx, domain.Movement{
			UserID: "u1", InvestmentID: stock.ID, Type: domain.MovementSell, Mode: domain.ModeUnits, Currency: "BRL",
			Date: day(t, "2026-11-02"), Quantity: 100_000_000, Amount: 1000,
		})
		require.NoError(t, err)
	}
	// Writes on them are not refused for what they already were.
	fee := f.move(t, stock, domain.MovementInput{Type: "fee", Date: "2026-11-03", Amount: amount(10)})
	require.NoError(t, f.svc.DeleteMovement(ctx, "u1", stock.ID, fee.ID))
	// A buy mends them; from then on, a write that breaks them is refused.
	buy := f.move(t, stock, domain.MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "1", Amount: amount(1000)})
	assert.ErrorIs(t, f.svc.DeleteMovement(ctx, "u1", stock.ID, buy.ID), domain.ErrExceedsPosition)
}

func TestUpdateMovement(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	stock := f.invest(t, domain.InvestmentStock, "")
	buy := f.move(t, stock, domain.MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "10", Amount: amount(10000)})
	f.move(t, stock, domain.MovementInput{Type: "sell", Date: "2026-11-03", Quantity: "8", Amount: amount(9000)})
	income := f.move(t, stock, domain.MovementInput{Type: "income", Date: "2026-11-04", Amount: amount(50), CategoryID: "dividends"})
	writes, hooks := f.movements.writes, f.hooks.count()

	got, err := f.svc.UpdateMovement(ctx, "u1", stock.ID, buy.ID, domain.MovementPatch{Quantity: ptr("10")}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, buy, got)
	assert.Equal(t, writes, f.movements.writes, "nothing changed, nothing written")

	got, err = f.svc.UpdateMovement(ctx, "u1", stock.ID, buy.ID, domain.MovementPatch{Quantity: ptr("9"), Fees: amount(20)}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, domain.Quantity(900_000_000), got.Quantity)
	assert.Equal(t, int64(20), got.Fees)
	assert.Equal(t, hooks, f.hooks.count(), "a buy changes no number of the dashboard")

	_, err = f.svc.UpdateMovement(ctx, "u1", stock.ID, buy.ID, domain.MovementPatch{Quantity: ptr("7")}, domain.Date{})
	assert.Equal(t, map[string]string{"quantity": domain.CodeExceedsPosition}, fieldsOf(t, err), "the later sale would sell more than held")
	_, err = f.svc.UpdateMovement(ctx, "u1", stock.ID, buy.ID, domain.MovementPatch{Date: ptr("2026-11-05")}, domain.Date{})
	assert.Equal(t, map[string]string{"quantity": domain.CodeExceedsPosition}, fieldsOf(t, err), "a buy moved after the sale")
	stored, err := f.svc.GetMovement(ctx, "u1", stock.ID, buy.ID)
	require.NoError(t, err)
	assert.Equal(t, "2026-11-01", stored.Date.String(), "nothing written")

	got, err = f.svc.UpdateMovement(ctx, "u1", stock.ID, income.ID, domain.MovementPatch{CategoryID: ptr("rent"), Amount: amount(60)}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, "rent", got.CategoryID)
	assert.Equal(t, hooks+1, f.hooks.count(), "an income changed")
	_, err = f.svc.UpdateMovement(ctx, "u1", stock.ID, income.ID, domain.MovementPatch{CategoryID: ptr("food")}, domain.Date{})
	assert.Equal(t, map[string]string{"categoryId": domain.CodeKindMismatch}, fieldsOf(t, err))
	_, err = f.svc.UpdateMovement(ctx, "u1", stock.ID, income.ID, domain.MovementPatch{Quantity: ptr("1")}, domain.Date{})
	assert.Equal(t, map[string]string{"quantity": domain.CodeInvalid}, fieldsOf(t, err), "an income has no quantity")

	other := f.invest(t, domain.InvestmentCrypto, "")
	_, err = f.svc.UpdateMovement(ctx, "u1", other.ID, buy.ID, domain.MovementPatch{Quantity: ptr("1")}, domain.Date{})
	assert.ErrorIs(t, err, domain.ErrNotFound, "through another investment")
	_, err = f.svc.UpdateMovement(ctx, "u2", stock.ID, buy.ID, domain.MovementPatch{Quantity: ptr("1")}, domain.Date{})
	assert.ErrorIs(t, err, domain.ErrNotFound, "another user's")
}

func TestDeleteMovement(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	stock := f.invest(t, domain.InvestmentStock, "")
	buy := f.move(t, stock, domain.MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "10", Amount: amount(10000)})
	sell := f.move(t, stock, domain.MovementInput{Type: "sell", Date: "2026-11-03", Quantity: "8", Amount: amount(9000)})
	fee := f.move(t, stock, domain.MovementInput{Type: "fee", Date: "2026-11-03", Amount: amount(10)})
	income := f.move(t, stock, domain.MovementInput{Type: "income", Date: "2026-11-04", Amount: amount(50), CategoryID: "dividends"})
	hooks := f.hooks.count()

	assert.ErrorIs(t, f.svc.DeleteMovement(ctx, "u1", stock.ID, buy.ID), domain.ErrExceedsPosition, "the sale relies on it")
	require.NoError(t, f.svc.DeleteMovement(ctx, "u1", stock.ID, fee.ID))
	assert.Equal(t, hooks, f.hooks.count())
	require.NoError(t, f.svc.DeleteMovement(ctx, "u1", stock.ID, income.ID))
	assert.Equal(t, hooks+1, f.hooks.count(), "an income left the dashboard")
	require.NoError(t, f.svc.DeleteMovement(ctx, "u1", stock.ID, sell.ID))
	require.NoError(t, f.svc.DeleteMovement(ctx, "u1", stock.ID, buy.ID), "nothing relies on it now")
	assert.ErrorIs(t, f.svc.DeleteMovement(ctx, "u1", stock.ID, buy.ID), domain.ErrNotFound)
	assert.ErrorIs(t, f.svc.DeleteMovement(ctx, "u2", stock.ID, buy.ID), domain.ErrNotFound)
}

func TestListMovements(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	stock := f.invest(t, domain.InvestmentStock, "")
	for i := range 5 {
		f.move(t, stock, domain.MovementInput{Type: "buy", Date: day(t, "2026-10-01").AddDays(i).String(), Quantity: "1", Amount: amount(1000)})
	}
	f.move(t, stock, domain.MovementInput{Type: "income", Date: "2026-10-20", Amount: amount(10), CategoryID: "dividends"})

	page, err := f.svc.ListMovements(ctx, "u1", stock.ID, MovementQuery{Limit: 4})
	require.NoError(t, err)
	require.Len(t, page.Items, 4)
	assert.Equal(t, domain.MovementIncome, page.Items[0].Type, "newest first")
	require.NotNil(t, page.Next)
	last := page.Items[3]
	assert.Equal(t, domain.MovementCursor{Date: last.Date, ID: last.ID}, *page.Next)
	page, err = f.svc.ListMovements(ctx, "u1", stock.ID, MovementQuery{Limit: 4, After: page.Next})
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
	assert.Nil(t, page.Next, "the last page")

	page, err = f.svc.ListMovements(ctx, "u1", stock.ID, MovementQuery{Filter: domain.MovementFilter{Type: domain.MovementIncome}})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)

	for _, limit := range []int{-1, MaxPageSize + 1} {
		_, err = f.svc.ListMovements(ctx, "u1", stock.ID, MovementQuery{Limit: limit})
		assert.Equal(t, map[string]string{"limit": domain.CodeOutOfRange}, fieldsOf(t, err), limit)
	}
	_, err = f.svc.ListMovements(ctx, "u2", stock.ID, MovementQuery{})
	assert.ErrorIs(t, err, domain.ErrNotFound)
	f.movements.pageErr = errBoom
	_, err = f.svc.ListMovements(ctx, "u1", stock.ID, MovementQuery{})
	assert.ErrorIs(t, err, errBoom)

	_, err = f.svc.GetMovement(ctx, "u2", stock.ID, page.Items[0].ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestMovementsByValue(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	cdb := f.invest(t, domain.InvestmentFixedIncome, "")
	f.move(t, cdb, domain.MovementInput{Type: "buy", Date: "2026-01-02", Amount: amount(100000)})
	// Redeeming past the value read closes the position: never refused.
	f.move(t, cdb, domain.MovementInput{Type: "sell", Date: "2026-11-02", Amount: amount(112000), Fees: amount(1800)})
	_, err := f.svc.CreateMovement(ctx, "u1", cdb.ID, domain.MovementInput{Type: "buy", Date: "2026-11-03", Quantity: "1", Amount: amount(1)}, domain.Date{})
	assert.Equal(t, map[string]string{"quantity": domain.CodeInvalid}, fieldsOf(t, err), "no units by value")

	ip, err := f.svc.Get(ctx, "u1", cdb.ID)
	require.NoError(t, err)
	assert.Zero(t, ip.Position.Value)
	assert.Equal(t, int64(112000-1800-100000), ip.Position.Realized)
}
