package domain

import (
	"cmp"
	"errors"
	"math/big"
	"slices"
)

// errPositionBounds means a sum of a position passes its bound (MaxAmount,
// MaxQuantity).
var errPositionBounds = errors.New("position out of bounds")

// IsPositionBounds reports whether err is BuildPosition's error for a sum
// past its bounds.
func IsPositionBounds(err error) bool { return errors.Is(err, errPositionBounds) }

// Returns is what money a position (or a portfolio, in one currency) put
// in, holds and got back, in minor units.
type Returns struct {
	// Value is what it is worth now: the units at the last price, or the
	// last value read on a statement plus what moved after it.
	Value int64
	// Invested is the cost of what is still held: at the average cost in
	// units mode (fees of the buys included, as the income tax counts it),
	// in proportion to the value in value mode.
	Invested int64
	// Contributed adds up every buy, fees included, sold since or not.
	Contributed int64
	// Realized is what the sales got back (less their fees) above the cost
	// of what they sold; negative for a loss.
	Realized int64
	// Income adds up what the asset paid; Fees the fees charged apart from
	// the trades.
	Income int64
	Fees   int64
}

// Unrealized is the gain of what is still held: Value less Invested.
func (r Returns) Unrealized() int64 { return r.Value - r.Invested }

// TotalReturn is every gain: unrealized, realized and income, less the
// fees charged apart.
func (r Returns) TotalReturn() int64 { return r.Unrealized() + r.Realized + r.Income - r.Fees }

// ReturnBps is TotalReturn over Contributed in basis points (1234 is
// 12.34%), rounded half away from zero; nil with nothing contributed. It
// is a simple return, not a time-weighted one.
func (r Returns) ReturnBps() *int64 {
	if r.Contributed <= 0 {
		return nil
	}
	// A portfolio's return times 10^4 may pass an int64.
	n := new(big.Int).Mul(big.NewInt(r.TotalReturn()), big.NewInt(10_000))
	neg := n.Sign() < 0
	n.Abs(n)
	v := divRound(n, big.NewInt(r.Contributed))
	// Like every number of the API, it stays within MaxAmount (a return of
	// 10^11 % on a few cents).
	out := MaxAmount
	if v.IsInt64() && v.Int64() < MaxAmount {
		out = v.Int64()
	}
	if neg {
		out = -out
	}
	return &out
}

// add sums o into r.
func (r *Returns) add(o Returns) {
	r.Value += o.Value
	r.Invested += o.Invested
	r.Contributed += o.Contributed
	r.Realized += o.Realized
	r.Income += o.Income
	r.Fees += o.Fees
}

// within reports whether every sum of r is within MaxAmount.
func (r Returns) within() bool {
	for _, v := range []int64{r.Value, r.Invested, r.Contributed, r.Realized, r.Income, r.Fees} {
		if v > MaxAmount || v < -MaxAmount {
			return false
		}
	}
	return true
}

// Position is what an investment holds after its movements.
type Position struct {
	Returns
	// Quantity is the units held, in units mode.
	Quantity Quantity
	// AveragePrice is the average cost of a unit held (Invested over
	// Quantity), in units mode while units are held.
	AveragePrice *Price
	// Price is the last price of a unit in units mode: a valuation's, or a
	// trade's (its amount over its quantity).
	Price *Price
	// ValuedOn is the day of the last quote: a valuation, or a trade in
	// units mode. Zero before any.
	ValuedOn Date
}

// InvestmentPosition is an investment with its position.
type InvestmentPosition struct {
	Investment Investment
	Position   Position
}

// movementOrder sorts the movements the way they apply: by day; within a
// day buys, sells, income, fees, then valuations (the close); then by id,
// which grows with creation (an unsaved movement, with no id, last).
func movementOrder(a, b Movement) int {
	if c := a.Date.t.Compare(b.Date.t); c != 0 {
		return c
	}
	if c := cmp.Compare(slices.Index(MovementTypes, a.Type), slices.Index(MovementTypes, b.Type)); c != 0 {
		return c
	}
	switch {
	case a.ID == b.ID:
		return 0
	case a.ID == "":
		return 1
	case b.ID == "":
		return -1
	}
	return cmp.Compare(a.ID, b.ID)
}

// BuildPosition replays the movements of an investment of mode in their
// order (movementOrder; the input is left as it is) and returns the
// position they make. It returns ErrExceedsPosition when a sale sells more
// units than held that day, or an error IsPositionBounds tells when a sum
// passes its bound; the position is still computed then (the sale sells
// what was held, sums saturate), so a read never fails on records a race
// let through, while a write is refused.
func BuildPosition(mode InvestmentMode, movements []Movement) (Position, error) {
	sorted := slices.Clone(movements)
	slices.SortStableFunc(sorted, movementOrder)
	b := positionBuilder{mode: mode}
	for _, m := range sorted {
		b.apply(m)
	}
	return b.finish()
}

// positionBuilder replays movements.
type positionBuilder struct {
	mode InvestmentMode
	pos  Position
	// value is the value in value mode: the last one read, plus what
	// moved after it.
	value int64
	err   error
}

// fail keeps the first error.
func (b *positionBuilder) fail(err error) {
	if b.err == nil {
		b.err = err
	}
}

func (b *positionBuilder) apply(m Movement) {
	p := &b.pos
	switch m.Type {
	case MovementBuy:
		cost := m.Amount + m.Fees
		p.Invested += cost
		p.Contributed += cost
		if b.mode == ModeUnits {
			p.Quantity += m.Quantity
			if p.Quantity > MaxQuantity {
				b.fail(errPositionBounds)
				p.Quantity = MaxQuantity
			}
			b.quote(m)
		} else {
			b.value += m.Amount
		}
	case MovementSell:
		if b.mode == ModeUnits {
			b.sellUnits(m)
			b.quote(m)
		} else {
			b.sellValue(m)
		}
	case MovementIncome:
		p.Income += m.Amount
	case MovementFee:
		p.Fees += m.Amount
	case MovementValuation:
		if b.mode == ModeUnits {
			price := m.Price
			p.Price = &price
		} else {
			b.value = m.Amount
		}
		p.ValuedOn = m.Date
	}
	if !p.within() || b.value > MaxAmount {
		b.fail(errPositionBounds)
	}
}

// quote takes the price of a trade as the last price.
func (b *positionBuilder) quote(m Movement) {
	if price, ok := PriceOf(m.Amount, m.Quantity); ok {
		b.pos.Price = &price
		b.pos.ValuedOn = m.Date
	}
}

// sellUnits sells m.Quantity at the average cost: selling everything takes
// the whole cost, so none is left over by rounding.
func (b *positionBuilder) sellUnits(m Movement) {
	p := &b.pos
	q := m.Quantity
	if q > p.Quantity {
		b.fail(ErrExceedsPosition)
		q = p.Quantity
	}
	out := p.Invested
	if q < p.Quantity {
		out = shareOf(p.Invested, int64(q), int64(p.Quantity))
	}
	p.Invested -= out
	p.Quantity -= q
	p.Realized += m.Amount - m.Fees - out
}

// sellValue redeems m.Amount in value mode, taking the cost in proportion
// to the value: redeeming the value or more (a deposit at maturity, with
// yield not read yet) closes the position.
func (b *positionBuilder) sellValue(m Movement) {
	p := &b.pos
	out := p.Invested
	if m.Amount < b.value {
		out = shareOf(p.Invested, m.Amount, b.value)
		b.value -= m.Amount
	} else {
		b.value = 0
	}
	p.Invested -= out
	p.Realized += m.Amount - m.Fees - out
}

// finish values the position.
func (b *positionBuilder) finish() (Position, error) {
	p := b.pos
	if b.mode == ModeValue {
		p.Value = b.value
		return p, b.err
	}
	if p.Quantity > 0 {
		if p.Price != nil {
			v, ok := p.Quantity.Value(*p.Price)
			if !ok {
				b.fail(errPositionBounds)
				v = MaxAmount
			}
			p.Value = v
		}
		if avg, ok := PriceOf(p.Invested, p.Quantity); ok {
			p.AveragePrice = &avg
		}
	}
	return p, b.err
}

// TypeValue is what a portfolio holds in one investment type.
type TypeValue struct {
	Type  InvestmentType
	Value int64
}

// PortfolioTotal adds up the positions of a portfolio in one currency, with
// no conversion between currencies.
type PortfolioTotal struct {
	Currency string
	Returns
	// ByType splits Value by investment type, the largest first.
	ByType []TypeValue
}

// Portfolio is the owner's investments with their positions and totals.
type Portfolio struct {
	Investments []InvestmentPosition
	Totals      []PortfolioTotal
}

// TotalPortfolio adds up positions by currency, in alphabetical order.
func TotalPortfolio(positions []InvestmentPosition) []PortfolioTotal {
	byCurrency := map[string]*PortfolioTotal{}
	byType := map[string]map[InvestmentType]int64{}
	for _, ip := range positions {
		c := ip.Investment.Currency
		t := byCurrency[c]
		if t == nil {
			t = &PortfolioTotal{Currency: c}
			byCurrency[c] = t
			byType[c] = map[InvestmentType]int64{}
		}
		t.add(ip.Position.Returns)
		byType[c][ip.Investment.Type] += ip.Position.Value
	}
	out := make([]PortfolioTotal, 0, len(byCurrency))
	for c, t := range byCurrency {
		t.ByType = make([]TypeValue, 0, len(byType[c]))
		for typ, v := range byType[c] {
			t.ByType = append(t.ByType, TypeValue{Type: typ, Value: v})
		}
		slices.SortFunc(t.ByType, func(a, b TypeValue) int {
			if c := cmp.Compare(b.Value, a.Value); c != 0 {
				return c
			}
			return cmp.Compare(a.Type, b.Type)
		})
		out = append(out, *t)
	}
	slices.SortFunc(out, func(a, b PortfolioTotal) int { return cmp.Compare(a.Currency, b.Currency) })
	return out
}
