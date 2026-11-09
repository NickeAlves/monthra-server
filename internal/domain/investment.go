package domain

import (
	"regexp"
	"strings"
	"time"
)

// Limits of investments.
const (
	MaxInvestmentNameLength = 100
	MaxTickerLength         = 20
	// MaxInvestments bounds the investments of one user; the list comes
	// back whole.
	MaxInvestments = 200
	// MaxMovementsPerInvestment bounds the movements of one investment,
	// which every read of its position goes through.
	MaxMovementsPerInvestment = 5000
)

// InvestmentType is the kind of asset (§1): what the portfolio groups by.
type InvestmentType string

// Supported investment types.
const (
	InvestmentFixedIncome    InvestmentType = "fixed_income"
	InvestmentStock          InvestmentType = "stock"
	InvestmentRealEstateFund InvestmentType = "real_estate_fund"
	InvestmentETF            InvestmentType = "etf"
	InvestmentCrypto         InvestmentType = "crypto"
	InvestmentFund           InvestmentType = "fund"
	InvestmentPension        InvestmentType = "pension"
	InvestmentOther          InvestmentType = "other"
)

// InvestmentTypes lists the supported investment types.
var InvestmentTypes = []InvestmentType{
	InvestmentFixedIncome, InvestmentStock, InvestmentRealEstateFund, InvestmentETF,
	InvestmentCrypto, InvestmentFund, InvestmentPension, InvestmentOther,
}

// ParseInvestmentType returns the investment type named s, if supported.
func ParseInvestmentType(s string) (InvestmentType, bool) {
	for _, t := range InvestmentTypes {
		if string(t) == s {
			return t, true
		}
	}
	return "", false
}

// InvestmentMode tells how a position is tracked.
type InvestmentMode string

// Investment modes.
const (
	// ModeUnits tracks a quantity of units and their price (stocks, funds
	// of the exchange, crypto): the value is the quantity at the last price.
	ModeUnits InvestmentMode = "units"
	// ModeValue tracks money in and out and the value the owner reads on
	// their statement (fixed income, pension): no units, no price.
	ModeValue InvestmentMode = "value"
)

// ParseInvestmentMode returns the investment mode named s, if supported.
func ParseInvestmentMode(s string) (InvestmentMode, bool) {
	switch m := InvestmentMode(s); m {
	case ModeUnits, ModeValue:
		return m, true
	}
	return "", false
}

// DefaultMode is the mode of an investment of type t when its owner names
// none: units for what trades on an exchange, value for the rest.
func (t InvestmentType) DefaultMode() InvestmentMode {
	switch t {
	case InvestmentStock, InvestmentRealEstateFund, InvestmentETF, InvestmentCrypto:
		return ModeUnits
	default:
		return ModeValue
	}
}

// Investment is an asset of the owner's portfolio (§1 item 7). The
// portfolio stands apart from the accounts: its movements never change an
// account's balance (user decision of 2026-11-09). Amounts are in minor
// units of Currency.
type Investment struct {
	ID     string
	UserID string
	Name   string
	Type   InvestmentType
	// Mode and Currency never change: the movements are read through them.
	Mode     InvestmentMode
	Currency string
	// Ticker is the exchange code, upper case, when the owner gave one; it
	// is not checked against any exchange.
	Ticker string
	// AccountID is the account the asset is kept at (a brokerage, a bank),
	// when the owner named one: for grouping only.
	AccountID string
	Source    Source
	CreatedAt time.Time
	UpdatedAt time.Time
}

// InvestmentInput is a new investment as the owner describes it.
type InvestmentInput struct {
	Name     string
	Type     string
	Mode     string
	Currency string
	Ticker   string
	// AccountID is "" for none.
	AccountID string
}

// InvestmentRefs is what the owner's store found for the account an input
// names (nil when it found none).
type InvestmentRefs struct {
	Account *Account
}

// tickerPattern is an exchange code, already in upper case.
var tickerPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.\-]*$`)

// normalizeTicker trims and upper-cases a ticker; empty is none.
func normalizeTicker(raw string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	switch {
	case s == "":
		return "", nil
	case len(s) > MaxTickerLength:
		return "", &fieldErr{CodeTooLong}
	case !tickerPattern.MatchString(s):
		return "", &fieldErr{CodeInvalid}
	}
	return s, nil
}

// investmentAccountRef checks the account an investment of currency is
// linked to (acc is what the owner's store found for id): one of the
// owner's, not a credit card, not archived, in the same currency.
func investmentAccountRef(v *Validation, id, currency string, acc *Account) {
	switch {
	case id == "":
	case acc == nil || acc.ID != id || acc.Type == AccountCreditCard:
		v.Add("accountId", CodeInvalid)
	case acc.Archived():
		v.Add("accountId", CodeArchived)
	case currency != "" && acc.Currency != currency:
		v.Add("accountId", CodeCurrencyMismatch)
	}
}

// NewInvestment validates in and returns the manual investment of userID,
// or a *ValidationError naming every invalid field.
func NewInvestment(userID string, in InvestmentInput, refs InvestmentRefs, now time.Time) (Investment, error) {
	var v Validation
	name, err := normalizeText(in.Name, MaxInvestmentNameLength)
	v.Check("name", err)

	typ, ok := ParseInvestmentType(in.Type)
	switch {
	case in.Type == "":
		v.Add("type", CodeRequired)
	case !ok:
		v.Add("type", CodeInvalid)
	}

	mode := typ.DefaultMode()
	if in.Mode != "" {
		m, ok := ParseInvestmentMode(in.Mode)
		if !ok {
			v.Add("mode", CodeInvalid)
		}
		mode = m
	}

	currency, ok := ParseCurrency(in.Currency)
	switch {
	case in.Currency == "":
		v.Add("currency", CodeRequired)
	case !ok:
		v.Add("currency", CodeInvalid)
	}

	ticker, err := normalizeTicker(in.Ticker)
	v.Check("ticker", err)
	investmentAccountRef(&v, in.AccountID, currency, refs.Account)
	if err := v.Err(); err != nil {
		return Investment{}, err
	}

	now = now.UTC()
	return Investment{
		UserID: userID, Name: name, Type: typ, Mode: mode, Currency: currency,
		Ticker: ticker, AccountID: in.AccountID, Source: SourceManual,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// InvestmentPatch changes some fields of an investment; nil fields stay as
// they are, and "" clears the ticker or the account. The mode and the
// currency are not fields: they never change.
type InvestmentPatch struct {
	Name      *string
	Type      *string
	Ticker    *string
	AccountID *string
}

// Apply checks the patch and returns current with the changes, whether
// anything changed (UpdatedAt moves only then), or a *ValidationError
// naming each invalid field. refs.Account is checked only when the patch
// moves the investment to another account: one archived since it was
// linked stays linked.
func (p InvestmentPatch) Apply(current Investment, refs InvestmentRefs, now time.Time) (Investment, bool, error) {
	var v Validation
	next := current
	if p.Name != nil {
		name, err := normalizeText(*p.Name, MaxInvestmentNameLength)
		v.Check("name", err)
		next.Name = name
	}
	if p.Type != nil {
		typ, ok := ParseInvestmentType(*p.Type)
		if !ok {
			v.Add("type", CodeInvalid)
		}
		next.Type = typ
	}
	if p.Ticker != nil {
		ticker, err := normalizeTicker(*p.Ticker)
		v.Check("ticker", err)
		next.Ticker = ticker
	}
	if p.AccountID != nil && *p.AccountID != current.AccountID {
		investmentAccountRef(&v, *p.AccountID, current.Currency, refs.Account)
		next.AccountID = *p.AccountID
	}
	if err := v.Err(); err != nil {
		return Investment{}, false, err
	}
	if next.Name == current.Name && next.Type == current.Type && next.Ticker == current.Ticker &&
		next.AccountID == current.AccountID {
		return current, false, nil
	}
	next.UpdatedAt = now.UTC()
	return next, true, nil
}

// MovementType is what a movement of an investment did.
type MovementType string

// Movement types.
const (
	// MovementBuy puts money in: units bought, or money applied.
	MovementBuy MovementType = "buy"
	// MovementSell takes money out: units sold, or money redeemed.
	MovementSell MovementType = "sell"
	// MovementIncome is what the asset paid (dividends, interest, rent):
	// it counts as income of its month on the dashboard.
	MovementIncome MovementType = "income"
	// MovementFee is a fee charged apart from a trade (custody, management).
	MovementFee MovementType = "fee"
	// MovementValuation is a quote: the price of a unit, or the value of
	// the position on a statement.
	MovementValuation MovementType = "valuation"
)

// MovementTypes lists the movement types, in the order a day applies them
// (BuildPosition): a valuation is the day's close.
var MovementTypes = []MovementType{MovementBuy, MovementSell, MovementIncome, MovementFee, MovementValuation}

// ParseMovementType returns the movement type named s, if supported.
func ParseMovementType(s string) (MovementType, bool) {
	for _, t := range MovementTypes {
		if string(t) == s {
			return t, true
		}
	}
	return "", false
}

// Movement is one entry of an investment's history. Which fields it holds
// depends on its type and on the investment's mode (Shape).
type Movement struct {
	ID           string
	UserID       string
	InvestmentID string
	// Type never changes, nor do Mode and Currency, copied from the
	// investment.
	Type     MovementType
	Mode     InvestmentMode
	Currency string
	Date     Date
	// Quantity is the units a trade bought or sold (units mode).
	Quantity Quantity
	// Price is the price of a valuation in units mode.
	Price Price
	// Amount is in minor units of Currency: what a trade bought or sold
	// for (before Fees), what an income paid, a fee, or the value of the
	// position on a valuation in value mode.
	Amount int64
	// Fees are what a trade cost on top (brokerage, exchange fees): a buy
	// pays them on top of Amount, a sell receives Amount less them.
	Fees int64
	// CategoryID is the income category an income counts under.
	CategoryID string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// MovementShape is which fields a movement takes.
type MovementShape struct {
	Quantity, Price, Amount, Fees, Category bool
}

// ShapeOf returns the fields a movement of type t takes on an investment of
// mode m:
//
//	type       units                     value
//	buy, sell  quantity, amount, fees?   amount, fees?
//	income     amount, categoryId        amount, categoryId
//	fee        amount                    amount
//	valuation  price                     amount (the position's value)
func ShapeOf(t MovementType, m InvestmentMode) MovementShape {
	switch t {
	case MovementBuy, MovementSell:
		return MovementShape{Quantity: m == ModeUnits, Amount: true, Fees: true}
	case MovementIncome:
		return MovementShape{Amount: true, Category: true}
	case MovementFee:
		return MovementShape{Amount: true}
	case MovementValuation:
		return MovementShape{Price: m == ModeUnits, Amount: m == ModeValue}
	}
	return MovementShape{}
}

// IsTrade reports whether m bought or sold.
func (m Movement) IsTrade() bool { return m.Type == MovementBuy || m.Type == MovementSell }

// MovementInput is a new movement as the owner describes it. Absent
// fields are "" or nil.
type MovementInput struct {
	Type string
	Date string
	// Quantity and Price are decimal strings (ParseQuantity, ParsePrice).
	Quantity   string
	Price      string
	Amount     *int64
	Fees       *int64
	CategoryID string
}

// MovementRefs is what the owner's store found for the category a movement
// names (nil when it found none).
type MovementRefs struct {
	Category *Category
}

// NewMovement validates in and returns a movement of inv for a reader whose
// today is today, or a *ValidationError naming every invalid field. A day
// after today is out of range: the portfolio records what happened. Whether
// the movement fits the position (a sale of more than held) is
// BuildPosition's to tell.
func NewMovement(inv Investment, in MovementInput, refs MovementRefs, today Date, now time.Time) (Movement, error) {
	var v Validation
	typ, ok := ParseMovementType(in.Type)
	switch {
	case in.Type == "":
		v.Add("type", CodeRequired)
	case !ok:
		v.Add("type", CodeInvalid)
	}
	m := Movement{
		UserID: inv.UserID, InvestmentID: inv.ID, Type: typ, Mode: inv.Mode, Currency: inv.Currency,
	}
	m.Date = movementDate(&v, in.Date, today)
	if ok {
		shape := ShapeOf(typ, inv.Mode)
		f := movementFields{
			quantity: optionalString(in.Quantity), price: optionalString(in.Price),
			amount: in.Amount, fees: in.Fees, category: optionalString(in.CategoryID),
		}
		f.apply(&v, shape, &m, refs, true)
		checkTradePrice(&v, m)
	}
	if err := v.Err(); err != nil {
		return Movement{}, err
	}
	now = now.UTC()
	m.CreatedAt, m.UpdatedAt = now, now
	return m, nil
}

// optionalString is nil for "" and &s otherwise.
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// movementDate checks the day of a movement: a Date up to today.
func movementDate(v *Validation, raw string, today Date) Date {
	d, err := ParseDate(raw)
	switch {
	case err != nil:
		v.Check("date", err)
	case today.Before(d):
		v.Add("date", CodeOutOfRange)
	}
	return d
}

// movementFields are the fields of a movement an input or a patch sets
// (nil when it sets none).
type movementFields struct {
	quantity, price *string
	amount, fees    *int64
	category        *string
}

// apply checks the fields given against shape and sets them on m: a field
// the shape does not take is invalid; on a new movement (required), a
// field it takes and that is missing is required (fees are optional).
func (f movementFields) apply(v *Validation, shape MovementShape, m *Movement, refs MovementRefs, required bool) {
	need := func(field string, takes, given bool) bool {
		switch {
		case !takes && given:
			v.Add(field, CodeInvalid)
		case takes && !given && required:
			v.Add(field, CodeRequired)
		}
		return takes && given
	}
	if need("quantity", shape.Quantity, f.quantity != nil) {
		q, err := ParseQuantity(*f.quantity)
		v.Check("quantity", err)
		m.Quantity = q
	}
	if need("price", shape.Price, f.price != nil) {
		p, err := ParsePrice(*f.price)
		v.Check("price", err)
		m.Price = p
	}
	if need("amount", shape.Amount, f.amount != nil) {
		if m.Type == MovementValuation {
			// A valuation may tell the position is worth nothing.
			v.Check("amount", CheckAmount(*f.amount, false))
		} else {
			v.Check("amount", checkPositiveAmount(*f.amount))
		}
		m.Amount = *f.amount
	}
	if f.fees != nil && need("fees", shape.Fees, true) {
		v.Check("fees", CheckAmount(*f.fees, false))
		m.Fees = *f.fees
	}
	if need("categoryId", shape.Category, f.category != nil) {
		incomeCategoryRef(v, *f.category, refs.Category)
		m.CategoryID = *f.category
	}
}

// incomeCategoryRef checks the category an income counts under (cat is what
// the owner's store found for id): an income category of the owner's.
func incomeCategoryRef(v *Validation, id string, cat *Category) {
	switch {
	case cat == nil || cat.ID != id:
		v.Add("categoryId", CodeInvalid)
	case cat.Kind != CategoryIncome:
		v.Add("categoryId", CodeKindMismatch)
	}
}

// checkTradePrice refuses a trade whose price per unit would pass MaxPrice
// (a dust quantity for a fortune), since the trade's price becomes the
// asset's quote.
func checkTradePrice(v *Validation, m Movement) {
	if !m.IsTrade() || m.Mode != ModeUnits || m.Quantity <= 0 || m.Amount <= 0 {
		return
	}
	if _, ok := PriceOf(m.Amount, m.Quantity); !ok {
		v.Add("amount", CodeOutOfRange)
	}
}

// MovementPatch changes some fields of a movement; nil fields stay as they
// are. The type is not a field: it never changes. Fields the movement's
// type does not take are invalid.
type MovementPatch struct {
	Date       *string
	Quantity   *string
	Price      *string
	Amount     *int64
	Fees       *int64
	CategoryID *string
}

// Apply checks the patch and returns current with the changes, whether
// anything changed (UpdatedAt moves only then), or a *ValidationError
// naming each invalid field. refs.Category is checked only when the patch
// moves the income to another category.
func (p MovementPatch) Apply(current Movement, refs MovementRefs, today Date, now time.Time) (Movement, bool, error) {
	var v Validation
	next := current
	if p.Date != nil {
		next.Date = movementDate(&v, *p.Date, today)
	}
	category := p.CategoryID
	if category != nil && *category == current.CategoryID {
		category = nil
	}
	if category != nil && *category == "" {
		v.Add("categoryId", CodeRequired)
		category = nil
	}
	f := movementFields{quantity: p.Quantity, price: p.Price, amount: p.Amount, fees: p.Fees, category: category}
	f.apply(&v, ShapeOf(current.Type, current.Mode), &next, refs, false)
	checkTradePrice(&v, next)
	if err := v.Err(); err != nil {
		return Movement{}, false, err
	}
	if next.Date.Equal(current.Date) && next.Quantity == current.Quantity && next.Price == current.Price &&
		next.Amount == current.Amount && next.Fees == current.Fees && next.CategoryID == current.CategoryID {
		return current, false, nil
	}
	next.UpdatedAt = now.UTC()
	return next, true, nil
}

// MovementFilter narrows a page of an investment's movements.
type MovementFilter struct {
	// Type is "" for every type.
	Type MovementType
}

// MovementCursor is where a page of movements ended, in their order:
// newest date first, then the newest id.
type MovementCursor = TransactionCursor
