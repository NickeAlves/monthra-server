package dto

import (
	"net/url"
	"strconv"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// CreateInvestmentRequest is the body of POST /investments. The domain
// checks every field, so missing ones are reported there.
type CreateInvestmentRequest struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Mode is units or value; empty takes the type's default.
	Mode     string `json:"mode"`
	Currency string `json:"currency"`
	Ticker   string `json:"ticker"`
	// AccountID is the account the asset is kept at, for grouping only.
	AccountID string `json:"accountId"`
}

// Input maps the request to the domain input.
func (r CreateInvestmentRequest) Input() domain.InvestmentInput {
	return domain.InvestmentInput{
		Name: r.Name, Type: r.Type, Mode: r.Mode, Currency: r.Currency, Ticker: r.Ticker, AccountID: r.AccountID,
	}
}

// UpdateInvestmentRequest is the body of PATCH /investments/{id}; absent
// fields stay as they are and "" clears the ticker or the account. The mode
// and the currency are not fields: they never change.
type UpdateInvestmentRequest struct {
	Name      *string `json:"name"`
	Type      *string `json:"type"`
	Ticker    *string `json:"ticker"`
	AccountID *string `json:"accountId"`
}

// Patch maps the request to the domain patch.
func (r UpdateInvestmentRequest) Patch() domain.InvestmentPatch {
	return domain.InvestmentPatch{Name: r.Name, Type: r.Type, Ticker: r.Ticker, AccountID: r.AccountID}
}

// Position is what an investment holds. Amounts are integers in minor units
// (cents) of its currency; quantities and prices are decimal strings with
// up to 8 places, prices in major units per unit ("38.42").
type Position struct {
	// Quantity is the units held (units mode; null by value).
	Quantity *string `json:"quantity"`
	// AveragePrice is the average cost of a unit held (null with none).
	AveragePrice *string `json:"averagePrice"`
	// Price is the last price of a unit: a valuation's or a trade's.
	Price *string `json:"price"`
	// ValuedOn is the day of the last quote (null before any).
	ValuedOn *string `json:"valuedOn"`
	Returns
}

// Returns is what a position or a portfolio put in, holds and got back.
type Returns struct {
	Value          int64 `json:"value"`
	Invested       int64 `json:"invested"`
	Contributed    int64 `json:"contributed"`
	UnrealizedGain int64 `json:"unrealizedGain"`
	RealizedGain   int64 `json:"realizedGain"`
	Income         int64 `json:"income"`
	Fees           int64 `json:"fees"`
	TotalReturn    int64 `json:"totalReturn"`
	// ReturnBps is TotalReturn over Contributed in basis points (1234 is
	// 12.34%), null with nothing contributed.
	ReturnBps *int64 `json:"returnBps"`
}

func newReturns(r domain.Returns) Returns {
	return Returns{
		Value: r.Value, Invested: r.Invested, Contributed: r.Contributed, UnrealizedGain: r.Unrealized(),
		RealizedGain: r.Realized, Income: r.Income, Fees: r.Fees, TotalReturn: r.TotalReturn(), ReturnBps: r.ReturnBps(),
	}
}

// Investment is the public view of an investment with its position.
type Investment struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Mode      string    `json:"mode"`
	Currency  string    `json:"currency"`
	Ticker    *string   `json:"ticker"`
	AccountID *string   `json:"accountId"`
	Source    string    `json:"source"`
	Position  Position  `json:"position"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// orNull is nil for "".
func orNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// NewInvestment maps an investment with its position.
func NewInvestment(ip domain.InvestmentPosition) Investment {
	inv, p := ip.Investment, ip.Position
	pos := Position{Returns: newReturns(p.Returns)}
	if inv.Mode == domain.ModeUnits {
		q := p.Quantity.String()
		pos.Quantity = &q
		if p.AveragePrice != nil {
			avg := p.AveragePrice.String()
			pos.AveragePrice = &avg
		}
		if p.Price != nil {
			price := p.Price.String()
			pos.Price = &price
		}
	}
	if !p.ValuedOn.IsZero() {
		d := p.ValuedOn.String()
		pos.ValuedOn = &d
	}
	return Investment{
		ID: inv.ID, Name: inv.Name, Type: string(inv.Type), Mode: string(inv.Mode), Currency: inv.Currency,
		Ticker: orNull(inv.Ticker), AccountID: orNull(inv.AccountID), Source: string(inv.Source),
		Position: pos, CreatedAt: inv.CreatedAt, UpdatedAt: inv.UpdatedAt,
	}
}

// TypeValue is what a portfolio holds in one investment type.
type TypeValue struct {
	Type  string `json:"type"`
	Value int64  `json:"value"`
}

// InvestmentTotal adds up the portfolio in one currency.
type InvestmentTotal struct {
	Currency string `json:"currency"`
	Returns
	// ByType splits value by investment type, the largest first.
	ByType []TypeValue `json:"byType"`
}

// Investments is the body of GET /investments: every investment, whole,
// and the totals by currency.
type Investments struct {
	Investments []Investment      `json:"investments"`
	Totals      []InvestmentTotal `json:"totals"`
}

// NewInvestments maps a portfolio; empty lists are [], never null.
func NewInvestments(p domain.Portfolio) Investments {
	out := Investments{Investments: make([]Investment, len(p.Investments)), Totals: make([]InvestmentTotal, len(p.Totals))}
	for i, ip := range p.Investments {
		out.Investments[i] = NewInvestment(ip)
	}
	for i, t := range p.Totals {
		total := InvestmentTotal{Currency: t.Currency, Returns: newReturns(t.Returns), ByType: make([]TypeValue, len(t.ByType))}
		for j, tv := range t.ByType {
			total.ByType[j] = TypeValue{Type: string(tv.Type), Value: tv.Value}
		}
		out.Totals[i] = total
	}
	return out
}

// CreateMovementRequest is the body of POST /investments/{id}/movements.
// Which fields a movement takes depends on its type and on the
// investment's mode; the domain checks them.
type CreateMovementRequest struct {
	Type string `json:"type"`
	Date string `json:"date"`
	// Quantity and Price are decimal strings, never numbers.
	Quantity   string `json:"quantity"`
	Price      string `json:"price"`
	Amount     *int64 `json:"amount"`
	Fees       *int64 `json:"fees"`
	CategoryID string `json:"categoryId"`
}

// Input maps the request to the domain input.
func (r CreateMovementRequest) Input() domain.MovementInput {
	return domain.MovementInput{
		Type: r.Type, Date: r.Date, Quantity: r.Quantity, Price: r.Price,
		Amount: r.Amount, Fees: r.Fees, CategoryID: r.CategoryID,
	}
}

// UpdateMovementRequest is the body of PATCH
// /investments/{id}/movements/{movementId}; absent fields stay as they are.
// The type is not a field: it never changes.
type UpdateMovementRequest struct {
	Date       *string `json:"date"`
	Quantity   *string `json:"quantity"`
	Price      *string `json:"price"`
	Amount     *int64  `json:"amount"`
	Fees       *int64  `json:"fees"`
	CategoryID *string `json:"categoryId"`
}

// Patch maps the request to the domain patch.
func (r UpdateMovementRequest) Patch() domain.MovementPatch {
	return domain.MovementPatch{
		Date: r.Date, Quantity: r.Quantity, Price: r.Price, Amount: r.Amount, Fees: r.Fees, CategoryID: r.CategoryID,
	}
}

// Movement is the public view of a movement; the fields its type does not
// take on the investment's mode are null.
type Movement struct {
	ID           string    `json:"id"`
	InvestmentID string    `json:"investmentId"`
	Type         string    `json:"type"`
	Date         string    `json:"date"`
	Quantity     *string   `json:"quantity"`
	Price        *string   `json:"price"`
	Amount       *int64    `json:"amount"`
	Fees         *int64    `json:"fees"`
	CategoryID   *string   `json:"categoryId"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// NewMovement maps a movement.
func NewMovement(m domain.Movement) Movement {
	out := Movement{
		ID: m.ID, InvestmentID: m.InvestmentID, Type: string(m.Type), Date: m.Date.String(),
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
	shape := domain.ShapeOf(m.Type, m.Mode)
	if shape.Quantity {
		q := m.Quantity.String()
		out.Quantity = &q
	}
	if shape.Price {
		p := m.Price.String()
		out.Price = &p
	}
	if shape.Amount {
		a := m.Amount
		out.Amount = &a
	}
	if shape.Fees {
		f := m.Fees
		out.Fees = &f
	}
	if shape.Category {
		out.CategoryID = orNull(m.CategoryID)
	}
	return out
}

// MovementsResponse is the body of GET /investments/{id}/movements: one
// page, and the cursor of the next one (null on the last page).
type MovementsResponse struct {
	Movements  []Movement `json:"movements"`
	NextCursor *string    `json:"nextCursor"`
}

// NewMovements maps a page; an empty page is [], never null.
func NewMovements(list []domain.Movement, next *domain.MovementCursor) MovementsResponse {
	out := MovementsResponse{Movements: make([]Movement, len(list))}
	for i, m := range list {
		out.Movements[i] = NewMovement(m)
	}
	if next != nil {
		c := EncodeCursor(*next)
		out.NextCursor = &c
	}
	return out
}

// MovementQuery is a parsed GET /investments/{id}/movements query.
type MovementQuery struct {
	Filter domain.MovementFilter
	After  *domain.MovementCursor
	// Limit is 0 when the query names none.
	Limit int
}

// ParseMovementQuery reads the type, the page size and the cursor of GET
// /investments/{id}/movements, or returns a *domain.ValidationError naming
// each invalid parameter. Empty parameters are ignored.
func ParseMovementQuery(q url.Values) (MovementQuery, error) {
	var v domain.Validation
	var out MovementQuery
	var err error
	if raw := q.Get("type"); raw != "" {
		typ, ok := domain.ParseMovementType(raw)
		if !ok {
			v.Add("type", domain.CodeInvalid)
		}
		out.Filter.Type = typ
	}
	if raw := q.Get("limit"); raw != "" {
		out.Limit, err = strconv.Atoi(raw)
		v.Check("limit", err)
	}
	if raw := q.Get("cursor"); raw != "" {
		out.After, err = DecodeCursor(raw)
		v.Check("cursor", err)
	}
	if err := v.Err(); err != nil {
		return MovementQuery{}, err
	}
	return out, nil
}
