package domain

import (
	"cmp"
	"slices"
)

// SummaryHistoryMonths is how many months the history of a summary
// covers, the month of the summary included.
const SummaryHistoryMonths = 12

// FlowSum is what the expenses or the income of one category added up to
// in one month and one currency, and how many transactions they were.
// Transfers are no flow: they move money between the owner's accounts.
type FlowSum struct {
	Month      Month
	Currency   string
	Kind       TransactionKind
	CategoryID string
	Amount     int64
	Count      int
}

// LegSum is what the transactions moved on one account in one month, in
// minor units of its currency: income in, expenses and transfers out,
// what a transfer brought in. The zero Month holds every month before the
// history of a summary.
type LegSum struct {
	AccountID string
	Month     Month
	Sum       int64
}

// Summary is the dashboard of one month (§1 item 2), by currency, with no
// conversion between them.
type Summary struct {
	Month Month
	// AsOf is the day the balances count up to: the last day of Month, or
	// the reader's today when that comes first.
	AsOf       Date
	Currencies []CurrencySummary
	// Accounts holds every account, archived ones included, oldest first.
	Accounts []AccountBalance
}

// CurrencySummary is the part of a summary in one currency.
type CurrencySummary struct {
	Currency string
	// Income and Expense add up the transactions of the whole month, the
	// days still to come included (an installment later in the month);
	// Net is Income less Expense.
	Income, Expense, Net int64
	// Scheduled is what the recurrences are still to create in the month.
	Scheduled Flows
	// Balance is the net worth on AsOf: what every account in the currency
	// holds, a credit card's debt counting against it.
	Balance            int64
	ExpensesByCategory []CategoryTotal
	IncomeByCategory   []CategoryTotal
	// History holds the SummaryHistoryMonths months up to Month, oldest
	// first.
	History []SummaryPoint
}

// Flows is money in (Income) and out (Expense).
type Flows struct {
	Income, Expense int64
}

// CategoryTotal is what one category added up to, and in how many
// transactions.
type CategoryTotal struct {
	CategoryID string
	Amount     int64
	Count      int
}

// SummaryPoint is one month of the history of a summary. NetWorth is the
// balance at the end of the month (on AsOf in its month), nil for a month
// after AsOf.
type SummaryPoint struct {
	Month           Month
	Income, Expense int64
	NetWorth        *int64
}

// AccountBalance is what one account holds on the day of a summary.
type AccountBalance struct {
	AccountID string
	Currency  string
	Balance   int64
}

// SummaryCut is the day the balances of the summary of month count up to
// for a reader whose today is today: the last day of month, or today when
// that comes first (a month still running, or one to come).
func SummaryCut(month Month, today Date) Date {
	if last := month.LastDay(); last.Before(today) {
		return last
	}
	return today
}

// BuildSummary puts together the summary of month for a reader whose today
// is today, from the owner's accounts and:
//   - flows: the FlowSums of the history's months;
//   - legs: the LegSums of the transactions up to SummaryCut(month, today),
//     the months before the history under the zero Month;
//   - scheduled: what the recurrences are still to create in month.
//
// A balance is the initial balance plus the transactions up to its day, as
// on the accounts; the initial balance has no day, so it counts in every
// month of the history.
func BuildSummary(month Month, today Date, accounts []Account, flows []FlowSum, legs []LegSum, scheduled []Transaction) Summary {
	cut := SummaryCut(month, today)
	first := month.AddMonths(1 - SummaryHistoryMonths)
	b := summaryBuilder{month: month, first: first, byCurrency: map[string]*CurrencySummary{}}

	currencyOf := make(map[string]string, len(accounts))
	balances := make(map[string]int64, len(accounts))
	// What each currency held before the history, then moved in each month.
	opening := map[string]int64{}
	moved := map[string][]int64{}
	for _, a := range accounts {
		currencyOf[a.ID] = a.Currency
		balances[a.ID] = a.InitialBalance
		opening[a.Currency] += a.InitialBalance
		b.currency(a.Currency)
	}
	for _, l := range legs {
		c, ok := currencyOf[l.AccountID]
		if !ok {
			// Only an account deleted while the summary was read.
			continue
		}
		i := l.Month.Sub(first)
		if i >= SummaryHistoryMonths && !l.Month.IsZero() {
			// After the summary's month: the legs come up to its cut only.
			continue
		}
		balances[l.AccountID] += l.Sum
		if l.Month.IsZero() || i < 0 {
			opening[c] += l.Sum
			continue
		}
		if moved[c] == nil {
			moved[c] = make([]int64, SummaryHistoryMonths)
		}
		moved[c][i] += l.Sum
	}
	for _, f := range flows {
		b.flow(f)
	}
	for _, t := range scheduled {
		if MonthOf(t.Date) != month {
			continue
		}
		c := b.currency(t.Currency)
		switch t.Kind {
		case TransactionIncome:
			c.Scheduled.Income += t.Amount
		case TransactionExpense:
			c.Scheduled.Expense += t.Amount
		}
	}

	out := Summary{Month: month, AsOf: cut, Currencies: []CurrencySummary{}, Accounts: make([]AccountBalance, len(accounts))}
	for i, a := range accounts {
		out.Accounts[i] = AccountBalance{AccountID: a.ID, Currency: a.Currency, Balance: balances[a.ID]}
	}
	last := MonthOf(cut)
	for _, code := range b.order() {
		c := b.byCurrency[code]
		c.Net = c.Income - c.Expense
		running := opening[code]
		for i := range c.History {
			if moved[code] != nil {
				running += moved[code][i]
			}
			if !last.Before(c.History[i].Month) {
				v := running
				c.History[i].NetWorth = &v
			}
		}
		c.Balance = running
		sortTotals(c.ExpensesByCategory)
		sortTotals(c.IncomeByCategory)
		out.Currencies = append(out.Currencies, *c)
	}
	return out
}

// summaryBuilder gathers the summary of each currency.
type summaryBuilder struct {
	month, first Month
	byCurrency   map[string]*CurrencySummary
}

// currency returns the summary of code, starting it on first use.
func (b *summaryBuilder) currency(code string) *CurrencySummary {
	if c, ok := b.byCurrency[code]; ok {
		return c
	}
	c := &CurrencySummary{
		Currency: code, ExpensesByCategory: []CategoryTotal{}, IncomeByCategory: []CategoryTotal{},
		History: make([]SummaryPoint, SummaryHistoryMonths),
	}
	for i := range c.History {
		c.History[i].Month = b.first.AddMonths(i)
	}
	b.byCurrency[code] = c
	return c
}

// flow adds f to its month of the history and, in the summary's month, to
// the totals and the categories.
func (b *summaryBuilder) flow(f FlowSum) {
	i := f.Month.Sub(b.first)
	if f.Month.IsZero() || i < 0 || i >= SummaryHistoryMonths {
		return
	}
	c := b.currency(f.Currency)
	p := &c.History[i]
	total := CategoryTotal{CategoryID: f.CategoryID, Amount: f.Amount, Count: f.Count}
	switch f.Kind {
	case TransactionIncome:
		p.Income += f.Amount
		if f.Month == b.month {
			c.Income += f.Amount
			c.IncomeByCategory = addTotal(c.IncomeByCategory, total)
		}
	case TransactionExpense:
		p.Expense += f.Amount
		if f.Month == b.month {
			c.Expense += f.Amount
			c.ExpensesByCategory = addTotal(c.ExpensesByCategory, total)
		}
	}
}

// order lists the currencies of the summary alphabetically.
func (b *summaryBuilder) order() []string {
	out := make([]string, 0, len(b.byCurrency))
	for code := range b.byCurrency {
		out = append(out, code)
	}
	slices.Sort(out)
	return out
}

// addTotal adds t to the total of its category in list.
func addTotal(list []CategoryTotal, t CategoryTotal) []CategoryTotal {
	for i := range list {
		if list[i].CategoryID == t.CategoryID {
			list[i].Amount += t.Amount
			list[i].Count += t.Count
			return list
		}
	}
	return append(list, t)
}

// sortTotals puts the largest totals first, then by category id.
func sortTotals(list []CategoryTotal) {
	slices.SortFunc(list, func(a, b CategoryTotal) int {
		if c := cmp.Compare(b.Amount, a.Amount); c != 0 {
			return c
		}
		return cmp.Compare(a.CategoryID, b.CategoryID)
	})
}
