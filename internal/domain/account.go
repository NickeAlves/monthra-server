package domain

import "time"

// Limits of the account fields.
const (
	MaxAccountNameLength = 100
	// MaxCardDay is the last day a card may close or fall due on; in a
	// shorter month the cycle uses the month's last day.
	MaxCardDay = 31
)

// AccountType is what kind of money an account holds.
type AccountType string

// Supported account types.
const (
	AccountChecking   AccountType = "checking"
	AccountSavings    AccountType = "savings"
	AccountCreditCard AccountType = "credit_card"
	AccountCash       AccountType = "cash"
	AccountBrokerage  AccountType = "brokerage"
)

// AccountTypes lists the supported account types.
var AccountTypes = []AccountType{AccountChecking, AccountSavings, AccountCreditCard, AccountCash, AccountBrokerage}

// ParseAccountType returns the account type named s, if it is supported.
func ParseAccountType(s string) (AccountType, bool) {
	for _, t := range AccountTypes {
		if string(t) == s {
			return t, true
		}
	}
	return "", false
}

// Source tells where a record came from (§5.6).
type Source string

// Record sources.
const (
	SourceManual      Source = "manual"
	SourceOpenFinance Source = "open_finance"
)

// CardSettings are the statement rules of a credit card account.
type CardSettings struct {
	// ClosingDay is the day of the month the statement closes (1 to 31).
	ClosingDay int
	// DueDay is the day of the month the statement falls due (1 to 31).
	DueDay int
	// Limit is the credit limit in minor units, when the owner set one.
	Limit *int64
}

// clone copies the settings, so an account never shares them with its input.
func (c *CardSettings) clone() *CardSettings {
	if c == nil {
		return nil
	}
	out := *c
	if c.Limit != nil {
		limit := *c.Limit
		out.Limit = &limit
	}
	return &out
}

// equal reports whether two settings hold the same values.
func (c *CardSettings) equal(o *CardSettings) bool {
	if c == nil || o == nil {
		return c == o
	}
	if c.ClosingDay != o.ClosingDay || c.DueDay != o.DueDay {
		return false
	}
	if c.Limit == nil || o.Limit == nil {
		return c.Limit == o.Limit
	}
	return *c.Limit == *o.Limit
}

// Account is a place where the owner keeps money: a bank account, a credit
// card, a wallet, a brokerage. Amounts are in minor units of Currency.
type Account struct {
	ID     string
	UserID string
	Name   string
	// Type and Currency never change: a new type or currency would read the
	// existing balances and statements in another way.
	Type     AccountType
	Currency string
	// InitialBalance is the balance before any transaction, in minor units;
	// it may be negative (an overdraft, a card's opening debt).
	InitialBalance int64
	// Card is set on credit card accounts, and only on them.
	Card   *CardSettings
	Source Source
	// ArchivedAt hides the account from the default list; its history stays.
	ArchivedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Balance is what an account holds, in minor units of its currency: its
// initial balance plus its transactions. Current counts the transactions up
// to a day (the reader's today); Projected counts them all, future ones
// included.
type Balance struct {
	Current   int64
	Projected int64
}

// Archived reports whether the owner archived the account.
func (a Account) Archived() bool { return a.ArchivedAt != nil }

// AccountInput is a new account as the owner describes it.
type AccountInput struct {
	Name           string
	Type           string
	Currency       string
	InitialBalance int64
	Card           *CardSettings
}

// NewAccount validates in and returns the manual account of userID, or a
// *ValidationError naming every invalid field.
func NewAccount(userID string, in AccountInput, now time.Time) (Account, error) {
	var v Validation
	name, err := normalizeText(in.Name, MaxAccountNameLength)
	v.Check("name", err)

	typ, ok := ParseAccountType(in.Type)
	switch {
	case in.Type == "":
		v.Add("type", CodeRequired)
	case !ok:
		v.Add("type", CodeInvalid)
	}

	currency, ok := ParseCurrency(in.Currency)
	switch {
	case in.Currency == "":
		v.Add("currency", CodeRequired)
	case !ok:
		v.Add("currency", CodeInvalid)
	}

	v.Check("initialBalance", CheckAmount(in.InitialBalance, true))
	if typ != "" {
		checkCard(&v, typ, in.Card)
	}
	if err := v.Err(); err != nil {
		return Account{}, err
	}

	now = now.UTC()
	return Account{
		UserID:         userID,
		Name:           name,
		Type:           typ,
		Currency:       currency,
		InitialBalance: in.InitialBalance,
		Card:           in.Card.clone(),
		Source:         SourceManual,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// checkCard records the card errors: settings are required on credit cards
// and refused on every other type.
func checkCard(v *Validation, typ AccountType, c *CardSettings) {
	if typ != AccountCreditCard {
		if c != nil {
			v.Add("card", CodeInvalid)
		}
		return
	}
	if c == nil {
		v.Add("card", CodeRequired)
		return
	}
	if c.ClosingDay < 1 || c.ClosingDay > MaxCardDay {
		v.Add("card.closingDay", CodeOutOfRange)
	}
	if c.DueDay < 1 || c.DueDay > MaxCardDay {
		v.Add("card.dueDay", CodeOutOfRange)
	}
	if c.Limit != nil {
		v.Check("card.limit", CheckAmount(*c.Limit, false))
	}
}

// AccountPatch changes some fields of an account; nil fields stay as they
// are. Card replaces the whole settings.
type AccountPatch struct {
	Name           *string
	InitialBalance *int64
	Card           *CardSettings
	Archived       *bool
}

// Apply checks the patch and returns current with the changes, whether
// anything changed (UpdatedAt moves only then), or a *ValidationError naming
// each invalid field. Archiving an archived account keeps its first date.
func (p AccountPatch) Apply(current Account, now time.Time) (Account, bool, error) {
	var v Validation
	next := current
	if p.Name != nil {
		name, err := normalizeText(*p.Name, MaxAccountNameLength)
		v.Check("name", err)
		next.Name = name
	}
	if p.InitialBalance != nil {
		v.Check("initialBalance", CheckAmount(*p.InitialBalance, true))
		next.InitialBalance = *p.InitialBalance
	}
	if p.Card != nil {
		checkCard(&v, current.Type, p.Card)
		next.Card = p.Card.clone()
	}
	now = now.UTC()
	if p.Archived != nil {
		switch {
		case *p.Archived && !current.Archived():
			next.ArchivedAt = &now
		case !*p.Archived:
			next.ArchivedAt = nil
		}
	}
	if err := v.Err(); err != nil {
		return Account{}, false, err
	}

	changed := next.Name != current.Name ||
		next.InitialBalance != current.InitialBalance ||
		!next.Card.equal(current.Card) ||
		next.Archived() != current.Archived()
	if !changed {
		return current, false, nil
	}
	next.UpdatedAt = now
	return next, true, nil
}
