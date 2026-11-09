package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var investmentNow = time.Date(2026, 11, 9, 10, 0, 0, 0, time.FixedZone("BRT", -3*3600))

func brokerage(id, currency string) *Account {
	return &Account{ID: id, UserID: "u1", Type: AccountBrokerage, Currency: currency}
}

func TestNewInvestment(t *testing.T) {
	inv, err := NewInvestment("u1", InvestmentInput{Name: "  Petro  ", Type: "stock", Currency: "BRL", Ticker: " petr4 "}, InvestmentRefs{}, investmentNow)
	require.NoError(t, err)
	assert.Equal(t, "u1", inv.UserID)
	assert.Equal(t, "Petro", inv.Name)
	assert.Equal(t, InvestmentStock, inv.Type)
	assert.Equal(t, ModeUnits, inv.Mode, "a stock trades in units by default")
	assert.Equal(t, "PETR4", inv.Ticker)
	assert.Empty(t, inv.AccountID)
	assert.Equal(t, SourceManual, inv.Source)
	assert.Equal(t, time.UTC, inv.CreatedAt.Location())
	assert.Equal(t, inv.CreatedAt, inv.UpdatedAt)

	inv, err = NewInvestment("u1", InvestmentInput{Name: "CDB", Type: "fixed_income", Currency: "BRL", AccountID: "a1"},
		InvestmentRefs{Account: brokerage("a1", "BRL")}, investmentNow)
	require.NoError(t, err)
	assert.Equal(t, ModeValue, inv.Mode, "fixed income is tracked by value")
	assert.Equal(t, "a1", inv.AccountID)

	inv, err = NewInvestment("u1", InvestmentInput{Name: "Tesouro", Type: "fixed_income", Mode: "units", Currency: "USD"}, InvestmentRefs{}, investmentNow)
	require.NoError(t, err)
	assert.Equal(t, ModeUnits, inv.Mode, "the owner may pick the mode")

	for _, typ := range InvestmentTypes {
		want := ModeValue
		switch typ {
		case InvestmentStock, InvestmentRealEstateFund, InvestmentETF, InvestmentCrypto:
			want = ModeUnits
		}
		assert.Equal(t, want, typ.DefaultMode(), typ)
	}
}

func TestNewInvestmentValidation(t *testing.T) {
	valid := InvestmentInput{Name: "Asset", Type: "stock", Currency: "BRL"}
	with := func(f func(*InvestmentInput)) InvestmentInput {
		in := valid
		f(&in)
		return in
	}
	archived := brokerage("a1", "BRL")
	at := investmentNow
	archived.ArchivedAt = &at
	card := &Account{ID: "a1", UserID: "u1", Type: AccountCreditCard, Currency: "BRL"}
	for name, tc := range map[string]struct {
		in   InvestmentInput
		acc  *Account
		want map[string]string
	}{
		"everything missing": {InvestmentInput{}, nil, map[string]string{"name": CodeRequired, "type": CodeRequired, "currency": CodeRequired}},
		"unknown type":       {with(func(in *InvestmentInput) { in.Type = "bond" }), nil, map[string]string{"type": CodeInvalid}},
		"unknown mode":       {with(func(in *InvestmentInput) { in.Mode = "shares" }), nil, map[string]string{"mode": CodeInvalid}},
		"unknown currency":   {with(func(in *InvestmentInput) { in.Currency = "GBP" }), nil, map[string]string{"currency": CodeInvalid}},
		"long name":          {with(func(in *InvestmentInput) { in.Name = strings.Repeat("a", 101) }), nil, map[string]string{"name": CodeTooLong}},
		"long ticker":        {with(func(in *InvestmentInput) { in.Ticker = "ABCDEFGHIJKLMNOPQRSTU" }), nil, map[string]string{"ticker": CodeTooLong}},
		"ticker spaces":      {with(func(in *InvestmentInput) { in.Ticker = "PETR 4" }), nil, map[string]string{"ticker": CodeInvalid}},
		"ticker symbol":      {with(func(in *InvestmentInput) { in.Ticker = "$PETR" }), nil, map[string]string{"ticker": CodeInvalid}},
		"ticker leading dot": {with(func(in *InvestmentInput) { in.Ticker = ".BTC" }), nil, map[string]string{"ticker": CodeInvalid}},
		"account not found":  {with(func(in *InvestmentInput) { in.AccountID = "a1" }), nil, map[string]string{"accountId": CodeInvalid}},
		"another account":    {with(func(in *InvestmentInput) { in.AccountID = "a2" }), brokerage("a1", "BRL"), map[string]string{"accountId": CodeInvalid}},
		"a card":             {with(func(in *InvestmentInput) { in.AccountID = "a1" }), card, map[string]string{"accountId": CodeInvalid}},
		"archived account":   {with(func(in *InvestmentInput) { in.AccountID = "a1" }), archived, map[string]string{"accountId": CodeArchived}},
		"other currency":     {with(func(in *InvestmentInput) { in.AccountID = "a1" }), brokerage("a1", "USD"), map[string]string{"accountId": CodeCurrencyMismatch}},
	} {
		_, err := NewInvestment("u1", tc.in, InvestmentRefs{Account: tc.acc}, investmentNow)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}
	_, err := NewInvestment("u1", with(func(in *InvestmentInput) { in.Ticker = "BRK.B" }), InvestmentRefs{}, investmentNow)
	assert.NoError(t, err, "dots and dashes in a ticker")
	_, err = NewInvestment("u1", with(func(in *InvestmentInput) { in.Ticker = "BTC-USD" }), InvestmentRefs{}, investmentNow)
	assert.NoError(t, err)
}

func TestInvestmentPatch(t *testing.T) {
	later := investmentNow.Add(time.Hour)
	current, err := NewInvestment("u1", InvestmentInput{Name: "Asset", Type: "stock", Currency: "BRL", Ticker: "ABC3", AccountID: "a1"},
		InvestmentRefs{Account: brokerage("a1", "BRL")}, investmentNow)
	require.NoError(t, err)

	next, changed, err := InvestmentPatch{}.Apply(current, InvestmentRefs{}, later)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, current, next)

	next, changed, err = InvestmentPatch{Name: ptr("Asset"), Ticker: ptr("abc3"), AccountID: ptr("a1")}.Apply(current, InvestmentRefs{}, later)
	require.NoError(t, err)
	assert.False(t, changed, "the same values, the account not looked up again")
	assert.Equal(t, current.UpdatedAt, next.UpdatedAt)

	next, changed, err = InvestmentPatch{Name: ptr("Renamed"), Type: ptr("etf"), Ticker: ptr(""), AccountID: ptr("")}.Apply(current, InvestmentRefs{}, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "Renamed", next.Name)
	assert.Equal(t, InvestmentETF, next.Type)
	assert.Equal(t, ModeUnits, next.Mode, "the mode stays")
	assert.Empty(t, next.Ticker, "cleared")
	assert.Empty(t, next.AccountID, "unlinked")
	assert.Equal(t, later.UTC(), next.UpdatedAt)

	next, changed, err = InvestmentPatch{AccountID: ptr("a2")}.Apply(current, InvestmentRefs{Account: brokerage("a2", "BRL")}, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "a2", next.AccountID)

	_, _, err = InvestmentPatch{Name: ptr(" "), Type: ptr("bond"), Ticker: ptr("a b"), AccountID: ptr("a2")}.Apply(current,
		InvestmentRefs{Account: brokerage("a2", "EUR")}, later)
	assert.Equal(t, map[string]string{
		"name": CodeRequired, "type": CodeInvalid, "ticker": CodeInvalid, "accountId": CodeCurrencyMismatch,
	}, fieldsOf(t, err))
}

// invOf returns an investment of mode m in BRL.
func invOf(m InvestmentMode) Investment {
	return Investment{ID: "i1", UserID: "u1", Name: "Asset", Type: InvestmentOther, Mode: m, Currency: "BRL"}
}

var (
	movementToday  = Date{time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}
	incomeCategory = &Category{ID: "c1", UserID: "u1", Kind: CategoryIncome}
)

func TestNewMovement(t *testing.T) {
	units, value := invOf(ModeUnits), invOf(ModeValue)
	m, err := NewMovement(units, MovementInput{Type: "buy", Date: "2026-11-09", Quantity: "10", Amount: ptr(int64(38420)), Fees: ptr(int64(500))},
		MovementRefs{}, movementToday, investmentNow)
	require.NoError(t, err)
	assert.Equal(t, "u1", m.UserID)
	assert.Equal(t, "i1", m.InvestmentID)
	assert.Equal(t, MovementBuy, m.Type)
	assert.Equal(t, ModeUnits, m.Mode)
	assert.Equal(t, "BRL", m.Currency)
	assert.Equal(t, "2026-11-09", m.Date.String(), "today is allowed")
	assert.Equal(t, Quantity(1_000_000_000), m.Quantity)
	assert.Equal(t, int64(38420), m.Amount)
	assert.Equal(t, int64(500), m.Fees)
	assert.True(t, m.IsTrade())
	assert.Equal(t, time.UTC, m.CreatedAt.Location())

	m, err = NewMovement(units, MovementInput{Type: "sell", Date: "2026-01-02", Quantity: "0.5", Amount: ptr(int64(100))}, MovementRefs{}, movementToday, investmentNow)
	require.NoError(t, err)
	assert.Zero(t, m.Fees, "fees are optional")

	m, err = NewMovement(units, MovementInput{Type: "valuation", Date: "2026-11-01", Price: "0"}, MovementRefs{}, movementToday, investmentNow)
	require.NoError(t, err)
	assert.Equal(t, Price(0), m.Price, "a worthless asset")

	m, err = NewMovement(value, MovementInput{Type: "valuation", Date: "2026-11-01", Amount: ptr(int64(0))}, MovementRefs{}, movementToday, investmentNow)
	require.NoError(t, err)
	assert.Zero(t, m.Amount)

	m, err = NewMovement(value, MovementInput{Type: "income", Date: "2026-11-01", Amount: ptr(int64(1500)), CategoryID: "c1"},
		MovementRefs{Category: incomeCategory}, movementToday, investmentNow)
	require.NoError(t, err)
	assert.Equal(t, "c1", m.CategoryID)
	assert.False(t, m.IsTrade())

	_, err = NewMovement(value, MovementInput{Type: "buy", Date: "2026-11-01", Amount: ptr(int64(100000)), Fees: ptr(int64(0))}, MovementRefs{}, movementToday, investmentNow)
	assert.NoError(t, err, "a buy by value takes no quantity")
	_, err = NewMovement(units, MovementInput{Type: "fee", Date: "2026-11-01", Amount: ptr(int64(990))}, MovementRefs{}, movementToday, investmentNow)
	assert.NoError(t, err)
}

func TestNewMovementValidation(t *testing.T) {
	units, value := invOf(ModeUnits), invOf(ModeValue)
	expense := &Category{ID: "c2", UserID: "u1", Kind: CategoryExpense}
	amount := func(v int64) *int64 { return &v }
	for name, tc := range map[string]struct {
		inv  Investment
		in   MovementInput
		refs MovementRefs
		want map[string]string
	}{
		"nothing":            {units, MovementInput{}, MovementRefs{}, map[string]string{"type": CodeRequired, "date": CodeRequired}},
		"unknown type":       {units, MovementInput{Type: "split", Date: "2026-11-01"}, MovementRefs{}, map[string]string{"type": CodeInvalid}},
		"future date":        {units, MovementInput{Type: "fee", Date: "2026-11-10", Amount: amount(1)}, MovementRefs{}, map[string]string{"date": CodeOutOfRange}},
		"bad date":           {units, MovementInput{Type: "fee", Date: "09/11/2026", Amount: amount(1)}, MovementRefs{}, map[string]string{"date": CodeInvalid}},
		"buy missing":        {units, MovementInput{Type: "buy", Date: "2026-11-01"}, MovementRefs{}, map[string]string{"quantity": CodeRequired, "amount": CodeRequired}},
		"buy zero amount":    {units, MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "1", Amount: amount(0)}, MovementRefs{}, map[string]string{"amount": CodeOutOfRange}},
		"buy bad quantity":   {units, MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "0", Amount: amount(1)}, MovementRefs{}, map[string]string{"quantity": CodeOutOfRange}},
		"buy negative fees":  {units, MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "1", Amount: amount(1), Fees: amount(-1)}, MovementRefs{}, map[string]string{"fees": CodeOutOfRange}},
		"buy with price":     {units, MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "1", Amount: amount(1), Price: "1"}, MovementRefs{}, map[string]string{"price": CodeInvalid}},
		"buy with category":  {units, MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "1", Amount: amount(1), CategoryID: "c1"}, MovementRefs{Category: incomeCategory}, map[string]string{"categoryId": CodeInvalid}},
		"dust for a fortune": {units, MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "0.00000001", Amount: amount(MaxAmount)}, MovementRefs{}, map[string]string{"amount": CodeOutOfRange}},
		"value buy quantity": {value, MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "1", Amount: amount(1)}, MovementRefs{}, map[string]string{"quantity": CodeInvalid}},
		"amount too large":   {value, MovementInput{Type: "sell", Date: "2026-11-01", Amount: amount(MaxAmount + 1)}, MovementRefs{}, map[string]string{"amount": CodeOutOfRange}},
		"income no category": {units, MovementInput{Type: "income", Date: "2026-11-01", Amount: amount(1)}, MovementRefs{}, map[string]string{"categoryId": CodeRequired}},
		"income unknown":     {units, MovementInput{Type: "income", Date: "2026-11-01", Amount: amount(1), CategoryID: "c9"}, MovementRefs{}, map[string]string{"categoryId": CodeInvalid}},
		"income expense cat": {units, MovementInput{Type: "income", Date: "2026-11-01", Amount: amount(1), CategoryID: "c2"}, MovementRefs{Category: expense}, map[string]string{"categoryId": CodeKindMismatch}},
		"income with fees":   {units, MovementInput{Type: "income", Date: "2026-11-01", Amount: amount(1), Fees: amount(0), CategoryID: "c1"}, MovementRefs{Category: incomeCategory}, map[string]string{"fees": CodeInvalid}},
		"fee with quantity":  {units, MovementInput{Type: "fee", Date: "2026-11-01", Amount: amount(1), Quantity: "1"}, MovementRefs{}, map[string]string{"quantity": CodeInvalid}},
		"units valuation":    {units, MovementInput{Type: "valuation", Date: "2026-11-01", Amount: amount(1)}, MovementRefs{}, map[string]string{"price": CodeRequired, "amount": CodeInvalid}},
		"value valuation":    {value, MovementInput{Type: "valuation", Date: "2026-11-01", Price: "1"}, MovementRefs{}, map[string]string{"price": CodeInvalid, "amount": CodeRequired}},
		"negative valuation": {value, MovementInput{Type: "valuation", Date: "2026-11-01", Amount: amount(-1)}, MovementRefs{}, map[string]string{"amount": CodeOutOfRange}},
		"bad price":          {units, MovementInput{Type: "valuation", Date: "2026-11-01", Price: "1.123456789"}, MovementRefs{}, map[string]string{"price": CodeInvalid}},
	} {
		_, err := NewMovement(tc.inv, tc.in, tc.refs, movementToday, investmentNow)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}
}

func TestMovementPatch(t *testing.T) {
	later := investmentNow.Add(time.Hour)
	units := invOf(ModeUnits)
	current, err := NewMovement(units, MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "10", Amount: ptr(int64(10000)), Fees: ptr(int64(100))},
		MovementRefs{}, movementToday, investmentNow)
	require.NoError(t, err)

	next, changed, err := MovementPatch{}.Apply(current, MovementRefs{}, movementToday, later)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, current, next)

	next, changed, err = MovementPatch{Quantity: ptr("10.0"), Amount: ptr(int64(10000))}.Apply(current, MovementRefs{}, movementToday, later)
	require.NoError(t, err)
	assert.False(t, changed, "the same values")
	assert.Equal(t, current.UpdatedAt, next.UpdatedAt)

	next, changed, err = MovementPatch{Date: ptr("2026-10-30"), Quantity: ptr("12.5"), Amount: ptr(int64(12500)), Fees: ptr(int64(0))}.Apply(current, MovementRefs{}, movementToday, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "2026-10-30", next.Date.String())
	assert.Equal(t, Quantity(1_250_000_000), next.Quantity)
	assert.Equal(t, int64(12500), next.Amount)
	assert.Zero(t, next.Fees)
	assert.Equal(t, MovementBuy, next.Type)
	assert.Equal(t, later.UTC(), next.UpdatedAt)

	_, _, err = MovementPatch{Date: ptr("2026-11-10"), Price: ptr("1"), CategoryID: ptr("c1"), Quantity: ptr("-1")}.Apply(current, MovementRefs{}, movementToday, later)
	assert.Equal(t, map[string]string{
		"date": CodeOutOfRange, "price": CodeInvalid, "categoryId": CodeInvalid, "quantity": CodeInvalid,
	}, fieldsOf(t, err))

	_, _, err = MovementPatch{Amount: ptr(MaxAmount)}.Apply(Movement{Type: MovementBuy, Mode: ModeUnits, Quantity: 1, Amount: 1}, MovementRefs{}, movementToday, later)
	assert.Equal(t, map[string]string{"amount": CodeOutOfRange}, fieldsOf(t, err), "the price of the patched trade is checked")

	income, err := NewMovement(units, MovementInput{Type: "income", Date: "2026-11-01", Amount: ptr(int64(100)), CategoryID: "c1"},
		MovementRefs{Category: incomeCategory}, movementToday, investmentNow)
	require.NoError(t, err)
	next, changed, err = MovementPatch{CategoryID: ptr("c1")}.Apply(income, MovementRefs{}, movementToday, later)
	require.NoError(t, err)
	assert.False(t, changed, "the same category, not looked up again")
	assert.Equal(t, income, next)
	other := &Category{ID: "c3", UserID: "u1", Kind: CategoryIncome}
	next, changed, err = MovementPatch{CategoryID: ptr("c3")}.Apply(income, MovementRefs{Category: other}, movementToday, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "c3", next.CategoryID)
	_, _, err = MovementPatch{CategoryID: ptr("")}.Apply(income, MovementRefs{}, movementToday, later)
	assert.Equal(t, map[string]string{"categoryId": CodeRequired}, fieldsOf(t, err))
	_, _, err = MovementPatch{CategoryID: ptr("c4")}.Apply(income, MovementRefs{Category: &Category{ID: "c4", Kind: CategoryExpense}}, movementToday, later)
	assert.Equal(t, map[string]string{"categoryId": CodeKindMismatch}, fieldsOf(t, err))
}

func TestParseMovementTypeAndMode(t *testing.T) {
	for _, typ := range MovementTypes {
		got, ok := ParseMovementType(string(typ))
		assert.True(t, ok)
		assert.Equal(t, typ, got)
	}
	_, ok := ParseMovementType("dividend")
	assert.False(t, ok)
	_, ok = ParseInvestmentMode("value")
	assert.True(t, ok)
	_, ok = ParseInvestmentMode("")
	assert.False(t, ok)
	assert.Equal(t, MovementShape{}, ShapeOf("split", ModeUnits))
}
