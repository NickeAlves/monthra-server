package dto

import (
	"net/url"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Summary is the body of GET /dashboard/summary: one month by currency,
// with no conversion between them. Amounts are integers in minor units
// (cents) of their currency. Accounts and categories come as ids: their
// names, icons and colors come from /accounts and /categories.
type Summary struct {
	// Month is the month summed, "YYYY-MM".
	Month string `json:"month"`
	// AsOf is the day the balances count up to: the month's last day, or
	// the reader's today when that comes first.
	AsOf       string            `json:"asOf"`
	Currencies []CurrencySummary `json:"currencies"`
	Accounts   []AccountBalance  `json:"accounts"`
}

// CurrencySummary is the part of a summary in one currency.
type CurrencySummary struct {
	Currency string `json:"currency"`
	// Income and Expense add up the whole month (days still to come
	// included); transfers are neither. Net is Income less Expense.
	Income  int64 `json:"income"`
	Expense int64 `json:"expense"`
	Net     int64 `json:"net"`
	// Scheduled is what the recurrences are still to create in the month.
	Scheduled Flows `json:"scheduled"`
	// Balance is the net worth on AsOf (a card's debt counts against it).
	Balance            int64           `json:"balance"`
	ExpensesByCategory []CategoryTotal `json:"expensesByCategory"`
	IncomeByCategory   []CategoryTotal `json:"incomeByCategory"`
	// History holds the 12 months up to Month, oldest first.
	History []SummaryPoint `json:"history"`
}

// Flows is money in and out.
type Flows struct {
	Income  int64 `json:"income"`
	Expense int64 `json:"expense"`
}

// CategoryTotal is what one category added up to, and in how many
// transactions; the largest first.
type CategoryTotal struct {
	CategoryID string `json:"categoryId"`
	Amount     int64  `json:"amount"`
	Count      int    `json:"count"`
}

// SummaryPoint is one month of the history; NetWorth is the balance at its
// end (on AsOf in its month), null for a month after AsOf.
type SummaryPoint struct {
	Month    string `json:"month"`
	Income   int64  `json:"income"`
	Expense  int64  `json:"expense"`
	NetWorth *int64 `json:"netWorth"`
}

// AccountBalance is what one account holds on AsOf; archived accounts
// included.
type AccountBalance struct {
	AccountID string `json:"accountId"`
	Currency  string `json:"currency"`
	Balance   int64  `json:"balance"`
}

// NewSummary maps a summary; empty lists are [], never null.
func NewSummary(s domain.Summary) Summary {
	out := Summary{
		Month: s.Month.String(), AsOf: s.AsOf.String(),
		Currencies: make([]CurrencySummary, len(s.Currencies)), Accounts: make([]AccountBalance, len(s.Accounts)),
	}
	for i, c := range s.Currencies {
		cs := CurrencySummary{
			Currency: c.Currency, Income: c.Income, Expense: c.Expense, Net: c.Net,
			Scheduled: Flows{Income: c.Scheduled.Income, Expense: c.Scheduled.Expense}, Balance: c.Balance,
			ExpensesByCategory: newCategoryTotals(c.ExpensesByCategory), IncomeByCategory: newCategoryTotals(c.IncomeByCategory),
			History: make([]SummaryPoint, len(c.History)),
		}
		for j, p := range c.History {
			cs.History[j] = SummaryPoint{Month: p.Month.String(), Income: p.Income, Expense: p.Expense, NetWorth: p.NetWorth}
		}
		out.Currencies[i] = cs
	}
	for i, a := range s.Accounts {
		out.Accounts[i] = AccountBalance{AccountID: a.AccountID, Currency: a.Currency, Balance: a.Balance}
	}
	return out
}

func newCategoryTotals(list []domain.CategoryTotal) []CategoryTotal {
	out := make([]CategoryTotal, len(list))
	for i, t := range list {
		out[i] = CategoryTotal{CategoryID: t.CategoryID, Amount: t.Amount, Count: t.Count}
	}
	return out
}

// MonthQuery is a parsed ?month&asOf query (GET /dashboard/summary, GET
// /budgets); zero fields were not given.
type MonthQuery struct {
	Month domain.Month
	AsOf  domain.Date
}

// ParseMonthQuery reads ?month=YYYY-MM&asOf=YYYY-MM-DD, or returns a
// *domain.ValidationError naming each invalid parameter.
func ParseMonthQuery(q url.Values) (MonthQuery, error) {
	var v domain.Validation
	var out MonthQuery
	var err error
	if raw := q.Get("month"); raw != "" {
		out.Month, err = domain.ParseMonth(raw)
		v.Check("month", err)
	}
	if raw := q.Get("asOf"); raw != "" {
		out.AsOf, err = domain.ParseDate(raw)
		v.Check("asOf", err)
	}
	if err := v.Err(); err != nil {
		return MonthQuery{}, err
	}
	return out, nil
}
