package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// units returns n whole units as a Quantity.
func units(n int64) Quantity { return Quantity(n * fixedOne) }

// trade returns a buy or sell of q units for amount, with fees.
func trade(t *testing.T, id string, typ MovementType, date string, q Quantity, amount, fees int64) Movement {
	return Movement{ID: id, Type: typ, Mode: ModeUnits, Date: day(t, date), Quantity: q, Amount: amount, Fees: fees}
}

func TestBuildPositionUnits(t *testing.T) {
	price150, err := ParsePrice("150")
	require.NoError(t, err)
	movements := []Movement{
		// Out of order on purpose: the replay sorts.
		{ID: "m06", Type: MovementValuation, Mode: ModeUnits, Date: day(t, "2026-11-05"), Price: price150},
		trade(t, "m01", MovementBuy, "2026-10-01", units(10), 100000, 500),
		trade(t, "m02", MovementBuy, "2026-10-10", units(10), 120000, 500),
		trade(t, "m03", MovementSell, "2026-10-20", units(5), 70000, 300),
		{ID: "m04", Type: MovementIncome, Mode: ModeUnits, Date: day(t, "2026-10-25"), Amount: 1000, CategoryID: "c1"},
		{ID: "m05", Type: MovementFee, Mode: ModeUnits, Date: day(t, "2026-10-31"), Amount: 200},
	}
	before := append([]Movement(nil), movements...)
	p, err := BuildPosition(ModeUnits, movements)
	require.NoError(t, err)
	assert.Equal(t, before, movements, "the input stays as it was")

	assert.Equal(t, units(15), p.Quantity)
	// Cost 2210.00 for 20 units (fees included); the sale takes 5/20 of it.
	assert.Equal(t, int64(221000), p.Contributed)
	assert.Equal(t, int64(165750), p.Invested)
	assert.Equal(t, int64(70000-300-55250), p.Realized)
	require.NotNil(t, p.AveragePrice)
	assert.Equal(t, "110.5", p.AveragePrice.String())
	require.NotNil(t, p.Price)
	assert.Equal(t, "150", p.Price.String(), "the last valuation")
	assert.Equal(t, "2026-11-05", p.ValuedOn.String())
	assert.Equal(t, int64(225000), p.Value)
	assert.Equal(t, int64(1000), p.Income)
	assert.Equal(t, int64(200), p.Fees)
	assert.Equal(t, int64(225000-165750), p.Unrealized())
	assert.Equal(t, int64(59250+14450+1000-200), p.TotalReturn())
	require.NotNil(t, p.ReturnBps())
	assert.Equal(t, int64(3371), *p.ReturnBps(), "74500 / 221000")
}

func TestBuildPositionTradePriceIsTheQuote(t *testing.T) {
	p, err := BuildPosition(ModeUnits, []Movement{
		trade(t, "m1", MovementBuy, "2026-10-01", units(3), 10000, 0),
	})
	require.NoError(t, err)
	require.NotNil(t, p.Price)
	assert.Equal(t, "33.33333333", p.Price.String())
	assert.Equal(t, int64(10000), p.Value, "99.99999999 rounds to 100.00")
	assert.Equal(t, "2026-10-01", p.ValuedOn.String())
}

func TestBuildPositionSellEverything(t *testing.T) {
	p, err := BuildPosition(ModeUnits, []Movement{
		trade(t, "m1", MovementBuy, "2026-10-01", units(3), 1000, 0),
		trade(t, "m2", MovementSell, "2026-10-02", units(1), 400, 0),
		trade(t, "m3", MovementSell, "2026-10-03", units(2), 900, 0),
	})
	require.NoError(t, err)
	assert.Zero(t, p.Quantity)
	assert.Zero(t, p.Invested, "no cost left over by rounding")
	assert.Zero(t, p.Value)
	assert.Nil(t, p.AveragePrice, "no units held")
	assert.Equal(t, int64((400-333)+(900-667)), p.Realized)
	assert.Equal(t, int64(300), p.TotalReturn())
}

func TestBuildPositionExceeds(t *testing.T) {
	p, err := BuildPosition(ModeUnits, []Movement{
		trade(t, "m1", MovementBuy, "2026-10-01", units(1), 1000, 0),
		trade(t, "m2", MovementSell, "2026-10-02", units(2), 3000, 0),
	})
	require.ErrorIs(t, err, ErrExceedsPosition)
	assert.False(t, IsPositionBounds(err))
	assert.Zero(t, p.Quantity, "the sale sold what was held")
	assert.Equal(t, int64(2000), p.Realized)

	_, err = BuildPosition(ModeUnits, []Movement{
		trade(t, "m1", MovementSell, "2026-10-01", units(1), 1000, 0),
		trade(t, "m2", MovementBuy, "2026-10-02", units(1), 1000, 0),
	})
	assert.ErrorIs(t, err, ErrExceedsPosition, "a sale before the buy")
}

func TestBuildPositionDayOrder(t *testing.T) {
	// A day trade written sell first: the day's buys apply before its sales.
	_, err := BuildPosition(ModeUnits, []Movement{
		trade(t, "m1", MovementSell, "2026-10-01", units(1), 1100, 0),
		trade(t, "m2", MovementBuy, "2026-10-01", units(1), 1000, 0),
	})
	require.NoError(t, err)

	nine, err := ParsePrice("9")
	require.NoError(t, err)
	p, err := BuildPosition(ModeUnits, []Movement{
		{ID: "m1", Type: MovementValuation, Mode: ModeUnits, Date: day(t, "2026-10-01"), Price: nine},
		trade(t, "m2", MovementBuy, "2026-10-01", units(1), 1000, 0),
	})
	require.NoError(t, err)
	assert.Equal(t, "9", p.Price.String(), "the day's valuation is its close, after its trades")
	assert.Equal(t, int64(900), p.Value)

	p, err = BuildPosition(ModeUnits, []Movement{
		{ID: "m1", Type: MovementValuation, Mode: ModeUnits, Date: day(t, "2026-10-01"), Price: nine},
		trade(t, "m2", MovementBuy, "2026-10-02", units(1), 1200, 0),
	})
	require.NoError(t, err)
	assert.Equal(t, "12", p.Price.String(), "a later trade is the newer quote")
	assert.Equal(t, "2026-10-02", p.ValuedOn.String())

	// The same day and type: by id, an unsaved movement last.
	p, err = BuildPosition(ModeUnits, []Movement{
		trade(t, "", MovementBuy, "2026-10-01", units(1), 1500, 0),
		trade(t, "m2", MovementBuy, "2026-10-01", units(1), 1200, 0),
		trade(t, "m1", MovementBuy, "2026-10-01", units(1), 1000, 0),
	})
	require.NoError(t, err)
	assert.Equal(t, "15", p.Price.String())
}

func TestBuildPositionValue(t *testing.T) {
	mv := func(id string, typ MovementType, date string, amount, fees int64) Movement {
		return Movement{ID: id, Type: typ, Mode: ModeValue, Date: day(t, date), Amount: amount, Fees: fees}
	}
	p, err := BuildPosition(ModeValue, []Movement{
		mv("m1", MovementBuy, "2026-01-05", 100000, 0),
		mv("m2", MovementBuy, "2026-02-10", 50000, 0),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(150000), p.Value, "no statement read yet: what went in")
	assert.Zero(t, p.Unrealized())
	assert.True(t, p.ValuedOn.IsZero())
	assert.Nil(t, p.Price)
	assert.Zero(t, p.Quantity)

	p, err = BuildPosition(ModeValue, []Movement{
		mv("m1", MovementBuy, "2026-01-05", 100000, 0),
		mv("m2", MovementBuy, "2026-02-10", 50000, 0),
		mv("m3", MovementValuation, "2026-03-31", 160000, 0),
		mv("m4", MovementSell, "2026-04-10", 80000, 0),
		mv("m5", MovementBuy, "2026-04-15", 10000, 0),
	})
	require.NoError(t, err)
	// The sale takes 80000/160000 of the cost: 75000.
	assert.Equal(t, int64(75000+10000), p.Invested)
	assert.Equal(t, int64(5000), p.Realized)
	assert.Equal(t, int64(160000-80000+10000), p.Value, "the last value read plus what moved after it")
	assert.Equal(t, "2026-03-31", p.ValuedOn.String())

	p, err = BuildPosition(ModeValue, []Movement{
		mv("m1", MovementBuy, "2026-01-05", 100000, 200),
		mv("m2", MovementSell, "2026-12-31", 112000, 1500),
	})
	require.NoError(t, err)
	assert.Zero(t, p.Value, "redeeming more than the value read closes the position")
	assert.Zero(t, p.Invested)
	assert.Equal(t, int64(112000-1500-100200), p.Realized)
	assert.Equal(t, int64(100200), p.Contributed)

	p, err = BuildPosition(ModeValue, []Movement{
		mv("m1", MovementBuy, "2026-01-05", 100000, 0),
		mv("m2", MovementValuation, "2026-02-01", 0, 0),
		mv("m3", MovementSell, "2026-03-01", 2500, 0),
	})
	require.NoError(t, err)
	assert.Zero(t, p.Invested, "a sale of a worthless position closes it")
	assert.Equal(t, int64(2500-100000), p.Realized)
	assert.Equal(t, int64(-97500), p.TotalReturn())
	assert.Equal(t, int64(-9750), *p.ReturnBps())
}

func TestBuildPositionBounds(t *testing.T) {
	_, err := BuildPosition(ModeValue, []Movement{
		{ID: "m1", Type: MovementBuy, Mode: ModeValue, Date: day(t, "2026-01-01"), Amount: MaxAmount},
		{ID: "m2", Type: MovementBuy, Mode: ModeValue, Date: day(t, "2026-01-02"), Amount: 1},
	})
	assert.True(t, IsPositionBounds(err))

	_, err = BuildPosition(ModeUnits, []Movement{
		trade(t, "m1", MovementBuy, "2026-01-01", MaxQuantity, 100, 0),
		trade(t, "m2", MovementBuy, "2026-01-02", 1, 100, 0),
	})
	assert.True(t, IsPositionBounds(err), "past MaxQuantity")

	top, err := ParsePrice("10000000000")
	require.NoError(t, err)
	p, err := BuildPosition(ModeUnits, []Movement{
		trade(t, "m1", MovementBuy, "2026-01-01", units(1000), 100, 0),
		{ID: "m2", Type: MovementValuation, Mode: ModeUnits, Date: day(t, "2026-01-02"), Price: top},
	})
	assert.True(t, IsPositionBounds(err), "worth past MaxAmount")
	assert.Equal(t, MaxAmount, p.Value, "saturated for reads")
}

func TestBuildPositionEmpty(t *testing.T) {
	p, err := BuildPosition(ModeUnits, nil)
	require.NoError(t, err)
	assert.Equal(t, Position{}, p)
	assert.Nil(t, p.ReturnBps(), "nothing contributed")

	p, err = BuildPosition(ModeValue, []Movement{{ID: "m1", Type: MovementValuation, Mode: ModeValue, Date: day(t, "2026-01-01"), Amount: 500}})
	require.NoError(t, err)
	assert.Equal(t, int64(500), p.Value)
	assert.Nil(t, p.ReturnBps())
}

func TestReturnBpsRounding(t *testing.T) {
	for _, tc := range []struct {
		r    Returns
		want int64
	}{
		{Returns{Value: 1, Contributed: 20000, Invested: 20000}, -10000}, // -9999.5
		{Returns{Income: 1, Contributed: 20000}, 1},                      // 0.5 rounds up
		{Returns{Fees: 1, Contributed: 20000}, -1},                       // -0.5 rounds away from zero
		{Returns{Income: 1, Contributed: 30000}, 0},                      // 0.33
		{Returns{Income: 2, Contributed: 3}, 6667},                       // 6666.67
		{Returns{Realized: -1, Contributed: 3}, -3333},
	} {
		got := tc.r.ReturnBps()
		require.NotNil(t, got, tc.r)
		assert.Equal(t, tc.want, *got, tc.r)
	}
	got := Returns{Value: 200 * MaxAmount, Contributed: 1}.ReturnBps()
	require.NotNil(t, got)
	assert.Equal(t, MaxAmount, *got, "a portfolio total times 10^4 passes an int64: bounded")
	got = Returns{Value: 200 * MaxAmount, Contributed: 200 * MaxAmount, Invested: 400 * MaxAmount}.ReturnBps()
	require.NotNil(t, got)
	assert.Equal(t, int64(-10000), *got, "no overflow on large totals")
}

func TestTotalPortfolio(t *testing.T) {
	pos := func(currency string, typ InvestmentType, value, invested int64) InvestmentPosition {
		return InvestmentPosition{
			Investment: Investment{Currency: currency, Type: typ},
			Position:   Position{Returns: Returns{Value: value, Invested: invested, Contributed: invested, Income: 10}},
		}
	}
	totals := TotalPortfolio([]InvestmentPosition{
		pos("USD", InvestmentCrypto, 500, 400),
		pos("BRL", InvestmentStock, 1000, 900),
		pos("BRL", InvestmentFixedIncome, 3000, 2800),
		pos("BRL", InvestmentStock, 2500, 2000),
		pos("BRL", InvestmentETF, 0, 0),
	})
	require.Len(t, totals, 2)
	brl := totals[0]
	assert.Equal(t, "BRL", brl.Currency)
	assert.Equal(t, int64(6500), brl.Value)
	assert.Equal(t, int64(5700), brl.Invested)
	assert.Equal(t, int64(40), brl.Income)
	assert.Equal(t, int64(840), brl.TotalReturn())
	assert.Equal(t, []TypeValue{
		{InvestmentStock, 3500}, {InvestmentFixedIncome, 3000}, {InvestmentETF, 0},
	}, brl.ByType, "the largest first")
	assert.Equal(t, "USD", totals[1].Currency)
	assert.Equal(t, []TypeValue{{InvestmentCrypto, 500}}, totals[1].ByType)

	assert.Empty(t, TotalPortfolio(nil))
}
