package investments

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

func TestCreate(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ip, err := f.svc.Create(ctx, "u1", domain.InvestmentInput{Name: "Stock", Type: "stock", Currency: "BRL", AccountID: "broker", Ticker: "abc3"})
	require.NoError(t, err)
	assert.NotEmpty(t, ip.Investment.ID)
	assert.Equal(t, "broker", ip.Investment.AccountID)
	assert.Equal(t, "ABC3", ip.Investment.Ticker)
	assert.Equal(t, t0, ip.Investment.CreatedAt)
	assert.Equal(t, domain.Position{}, ip.Position, "nothing held yet")

	for name, tc := range map[string]struct {
		account string
		want    string
	}{
		"another user's":  {"theirs", domain.CodeInvalid},
		"unknown":         {"nope", domain.CodeInvalid},
		"a card":          {"card", domain.CodeInvalid},
		"archived":        {"old", domain.CodeArchived},
		"other currency":  {"dollars", domain.CodeCurrencyMismatch},
		"a category's id": {"food", domain.CodeInvalid},
	} {
		_, err := f.svc.Create(ctx, "u1", domain.InvestmentInput{Name: "X", Type: "stock", Currency: "BRL", AccountID: tc.account})
		assert.Equal(t, map[string]string{"accountId": tc.want}, fieldsOf(t, err), name)
	}
	_, err = f.svc.Create(ctx, "u1", domain.InvestmentInput{Name: "Cash", Type: "other", Currency: "BRL", AccountID: "elsewhen"})
	assert.NoError(t, err, "any account but a card")

	full := domain.MaxInvestments
	f.store.count = &full
	_, err = f.svc.Create(ctx, "u1", domain.InvestmentInput{Name: "X", Type: "stock", Currency: "BRL"})
	assert.ErrorIs(t, err, domain.ErrLimitReached)
	f.store.count = nil

	f.store.countErr = errBoom
	_, err = f.svc.Create(ctx, "u1", domain.InvestmentInput{Name: "X", Type: "stock", Currency: "BRL"})
	assert.ErrorIs(t, err, errBoom)
	f.store.countErr = nil

	failing := newFixture(t)
	failing.refs.err = errBoom
	failing.svc = NewService(failing.store, failing.movements, fakeAccounts{*failing.refs}, fakeCategories{*failing.refs}, Options{})
	_, err = failing.svc.Create(ctx, "u1", domain.InvestmentInput{Name: "X", Type: "stock", Currency: "BRL", AccountID: "broker"})
	assert.ErrorIs(t, err, errBoom, "a lookup failure is no validation error")
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ip, err := f.svc.Create(ctx, "u1", domain.InvestmentInput{Name: "Stock", Type: "stock", Currency: "BRL", AccountID: "broker"})
	require.NoError(t, err)
	inv := ip.Investment
	f.move(t, inv, domain.MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "2", Amount: amount(2000)})
	writes := f.store.writes

	// The account archived since it was linked stays linked through a patch
	// that does not move it.
	got, err := f.svc.Update(ctx, "u1", inv.ID, domain.InvestmentPatch{Name: ptr("Stock"), AccountID: ptr("broker")})
	require.NoError(t, err)
	assert.Equal(t, writes, f.store.writes, "nothing changed, nothing written")
	assert.Equal(t, int64(2000), got.Position.Value, "returned with its position")

	got, err = f.svc.Update(ctx, "u1", inv.ID, domain.InvestmentPatch{Name: ptr("Renamed"), AccountID: ptr("")})
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.Investment.Name)
	assert.Empty(t, got.Investment.AccountID)
	assert.Equal(t, writes+1, f.store.writes)

	_, err = f.svc.Update(ctx, "u1", inv.ID, domain.InvestmentPatch{AccountID: ptr("dollars")})
	assert.Equal(t, map[string]string{"accountId": domain.CodeCurrencyMismatch}, fieldsOf(t, err))
	_, err = f.svc.Update(ctx, "u2", inv.ID, domain.InvestmentPatch{Name: ptr("Mine")})
	assert.ErrorIs(t, err, domain.ErrNotFound, "another user's")
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	inv := f.invest(t, domain.InvestmentStock, "")
	f.move(t, inv, domain.MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "1", Amount: amount(1000)})
	f.move(t, inv, domain.MovementInput{Type: "income", Date: "2026-11-02", Amount: amount(10), CategoryID: "dividends"})
	hooks := f.hooks.count()

	assert.ErrorIs(t, f.svc.Delete(ctx, "u2", inv.ID), domain.ErrNotFound)
	assert.Equal(t, hooks, f.hooks.count(), "nothing deleted, nothing told")

	require.NoError(t, f.svc.Delete(ctx, "u1", inv.ID))
	assert.Equal(t, 2, f.movements.deleteOfs, "the movements before and after the investment")
	assert.Equal(t, 1, f.store.deletes)
	assert.Empty(t, f.movements.byID)
	assert.Equal(t, hooks+1, f.hooks.count(), "its income leaves the dashboard")
	_, err := f.svc.Get(ctx, "u1", inv.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestListAndGet(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	stock := f.invest(t, domain.InvestmentStock, "")
	cdb := f.invest(t, domain.InvestmentFixedIncome, "")
	f.move(t, stock, domain.MovementInput{Type: "buy", Date: "2026-10-01", Quantity: "10", Amount: amount(10000), Fees: amount(100)})
	f.move(t, stock, domain.MovementInput{Type: "valuation", Date: "2026-11-01", Price: "12"})
	f.move(t, cdb, domain.MovementInput{Type: "buy", Date: "2026-10-01", Amount: amount(50000)})
	f.move(t, cdb, domain.MovementInput{Type: "valuation", Date: "2026-11-01", Amount: amount(50500)})
	other, err := f.svc.Create(ctx, "u2", domain.InvestmentInput{Name: "Theirs", Type: "crypto", Currency: "BRL"})
	require.NoError(t, err)

	p, err := f.svc.List(ctx, "u1")
	require.NoError(t, err)
	require.Len(t, p.Investments, 2)
	assert.Equal(t, stock.ID, p.Investments[0].Investment.ID, "oldest first")
	assert.Equal(t, int64(12000), p.Investments[0].Position.Value)
	assert.Equal(t, int64(10100), p.Investments[0].Position.Invested)
	assert.Equal(t, int64(50500), p.Investments[1].Position.Value)
	require.Len(t, p.Totals, 1)
	assert.Equal(t, int64(62500), p.Totals[0].Value)
	assert.Equal(t, int64(62500-60100), p.Totals[0].TotalReturn())
	assert.Equal(t, []domain.TypeValue{{Type: domain.InvestmentFixedIncome, Value: 50500}, {Type: domain.InvestmentStock, Value: 12000}}, p.Totals[0].ByType)

	got, err := f.svc.Get(ctx, "u1", stock.ID)
	require.NoError(t, err)
	assert.Equal(t, p.Investments[0], got)
	_, err = f.svc.Get(ctx, "u1", other.Investment.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound, "another user's")

	// Records a race let through still list.
	_, err = f.movements.Insert(ctx, domain.Movement{
		UserID: "u1", InvestmentID: stock.ID, Type: domain.MovementSell, Mode: domain.ModeUnits, Currency: "BRL",
		Date: day(t, "2026-11-02"), Quantity: 50 * 100_000_000, Amount: 1,
	})
	require.NoError(t, err)
	p, err = f.svc.List(ctx, "u1")
	require.NoError(t, err)
	assert.Zero(t, p.Investments[0].Position.Quantity, "sold what was held")

	empty, err := f.svc.List(ctx, "u3")
	require.NoError(t, err)
	assert.Empty(t, empty.Investments)
	assert.Empty(t, empty.Totals)

	f.movements.allErr = errBoom
	_, err = f.svc.List(ctx, "u1")
	assert.ErrorIs(t, err, errBoom)
}

func day(t *testing.T, s string) domain.Date {
	t.Helper()
	d, err := domain.ParseDate(s)
	require.NoError(t, err)
	return d
}
