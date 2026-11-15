package domain

import (
	"cmp"
	"slices"
)

const (
	// ReportMaxMonths bounds the months one report covers.
	ReportMaxMonths = 60
	// ReportDefaultMonths is how many months a report without a start
	// covers, its end included.
	ReportDefaultMonths = 12
)

// Period is a run of whole months, From through To.
type Period struct {
	From, To Month
}

// Months returns how many months p covers.
func (p Period) Months() int { return p.To.Sub(p.From) + 1 }

// Contains reports whether month m lies in p.
func (p Period) Contains(m Month) bool { return !m.Before(p.From) && !p.To.Before(m) }

// Previous returns the period of the same length that ends right before p.
func (p Period) Previous() Period {
	n := p.Months()
	return Period{From: p.From.AddMonths(-n), To: p.From.AddMonths(-1)}
}

// ReportPeriod returns the period of a report from month from through
// month to for a reader whose today is today. A zero to is today's month
// (or from, when that comes later), a zero from the ReportDefaultMonths
// months up to to. A period ending before it starts is "to: invalid", one
// longer than ReportMaxMonths "to: out_of_range".
func ReportPeriod(from, to Month, today Date) (Period, error) {
	if to.IsZero() {
		to = MonthOf(today)
		if !from.IsZero() && to.Before(from) {
			to = from
		}
	}
	if from.IsZero() {
		from = to.AddMonths(1 - ReportDefaultMonths)
	}
	var v Validation
	switch {
	case to.Before(from):
		v.Add("to", CodeInvalid)
	case to.Sub(from) >= ReportMaxMonths:
		v.Add("to", CodeOutOfRange)
	}
	if err := v.Err(); err != nil {
		return Period{}, err
	}
	return Period{From: from, To: to}, nil
}

// AccountFlowSum is what the expenses or the income on one account added up
// to over a period, and how many transactions they were.
type AccountFlowSum struct {
	Currency  string
	Kind      TransactionKind
	AccountID string
	Amount    int64
	Count     int
}

// TagFlowSum is what the expenses or the income with one tag added up to
// over a period, and how many transactions they were. A transaction with
// several tags counts under each of them.
type TagFlowSum struct {
	Currency string
	Kind     TransactionKind
	Tag      string
	Amount   int64
	Count    int
}

// Report is the income and expenses of a period (§1 item 8), by currency,
// with no conversion between them, beside the period of the same length
// before it.
type Report struct {
	Period   Period
	Previous Period
	// AsOf is the day the net worth counts up to: the last day of the
	// period, or the reader's today when that comes first.
	AsOf       Date
	Currencies []CurrencyReport
}

// CurrencyReport is the part of a report in one currency. Its flows are the
// expenses and income of the whole months, as on the dashboard: transfers
// are none, and what the investments paid counts as income.
type CurrencyReport struct {
	Currency string
	PeriodTotals
	// InvestmentIncome is the part of Income the investments paid. It
	// counts in Months and IncomeByCategory, but in no account or tag.
	InvestmentIncome int64
	// Previous sums the period before, the investments' income included.
	Previous PeriodTotals
	// Months holds each month of the period, oldest first.
	Months             []ReportMonth
	ExpensesByCategory []CategoryComparison
	IncomeByCategory   []CategoryComparison
	// ByAccount holds the accounts with transactions in the period.
	ByAccount     []AccountTotal
	ExpensesByTag []TagTotal
	IncomeByTag   []TagTotal
}

// PeriodTotals adds up the flows of a period. SavingsRateBps is Net over
// Income in basis points, rounded half away from zero (negative when more
// went out than came in), nil without income.
type PeriodTotals struct {
	Income, Expense, Net int64
	SavingsRateBps       *int64
}

// ReportMonth is one month of a report. NetWorth is what the accounts held
// at its end (on AsOf in its month), nil for a month after AsOf.
type ReportMonth struct {
	Month                Month
	Income, Expense, Net int64
	NetWorth             *int64
}

// CategoryComparison is what one category added up to in the period, in
// how many transactions, and what it added up to in the period before.
type CategoryComparison struct {
	CategoryID     string
	Amount         int64
	Count          int
	PreviousAmount int64
}

// AccountTotal is the income and expenses of one account in a period, and
// how many transactions they were.
type AccountTotal struct {
	AccountID       string
	Income, Expense int64
	Count           int
}

// TagTotal is what one tag added up to, and in how many transactions.
type TagTotal struct {
	Tag    string
	Amount int64
	Count  int
}

// ReportInput is what a report is built from.
type ReportInput struct {
	Period Period
	// Today is the reader's today.
	Today Date
	// Accounts holds every account of the owner, archived ones included.
	Accounts []Account
	// Flows holds the FlowSums of the transactions, and Income the ones of
	// the investments' income, of the period before and the period.
	Flows, Income []FlowSum
	// ByAccount and ByTag sum the transactions of the period.
	ByAccount []AccountFlowSum
	ByTag     []TagFlowSum
	// Legs holds the LegSums of the transactions up to the report's AsOf,
	// the months before the period under the zero Month.
	Legs []LegSum
}

// BuildReport puts together the report of in.Period. A balance counts as on
// the dashboard (BuildSummary): the initial balance plus the transactions
// up to its day, a card's debt against it.
func BuildReport(in ReportInput) Report {
	p := in.Period
	cut := SummaryCut(p.To, in.Today)
	b := reportBuilder{period: p, previous: p.Previous(), byCurrency: map[string]*currencyReport{}}
	for _, a := range in.Accounts {
		b.currency(a.Currency)
	}
	for _, f := range in.Flows {
		b.flow(f, false)
	}
	for _, f := range in.Income {
		b.flow(f, true)
	}
	for _, f := range in.ByAccount {
		b.account(f)
	}
	for _, f := range in.ByTag {
		b.tag(f)
	}

	worth := newNetWorth(in.Accounts, in.Legs, p.From, p.Months())
	last := MonthOf(cut)
	out := Report{Period: p, Previous: b.previous, AsOf: cut, Currencies: []CurrencyReport{}}
	for _, code := range sortedKeys(b.byCurrency) {
		c := b.byCurrency[code]
		// The totals of the period, then of the one before.
		c.close()
		c.Previous.close()
		series, _ := worth.series(code, last)
		for i := range c.Months {
			m := &c.Months[i]
			m.Net = m.Income - m.Expense
			m.NetWorth = series[i]
		}
		c.ExpensesByCategory = sortedCategories(c.expenses)
		c.IncomeByCategory = sortedCategories(c.income)
		c.ByAccount = sortedAccounts(c.accounts)
		c.ExpensesByTag = sortedTags(c.expenseTags)
		c.IncomeByTag = sortedTags(c.incomeTags)
		out.Currencies = append(out.Currencies, c.CurrencyReport)
	}
	return out
}

// close sets Net and SavingsRateBps from Income and Expense.
func (t *PeriodTotals) close() {
	t.Net = t.Income - t.Expense
	t.SavingsRateBps = nil
	if t.Income > 0 {
		t.SavingsRateBps = bps(t.Net, t.Income)
	}
}

// reportBuilder gathers the report of each currency.
type reportBuilder struct {
	period, previous Period
	byCurrency       map[string]*currencyReport
}

// currencyReport is a CurrencyReport being built, its lists still keyed.
type currencyReport struct {
	CurrencyReport
	expenses, income        map[string]*CategoryComparison
	accounts                map[string]*AccountTotal
	expenseTags, incomeTags map[string]*TagTotal
}

// currency returns the report of code, starting it on first use.
func (b *reportBuilder) currency(code string) *currencyReport {
	if c, ok := b.byCurrency[code]; ok {
		return c
	}
	c := &currencyReport{
		CurrencyReport: CurrencyReport{Currency: code, Months: make([]ReportMonth, b.period.Months())},
		expenses:       map[string]*CategoryComparison{}, income: map[string]*CategoryComparison{},
		accounts:    map[string]*AccountTotal{},
		expenseTags: map[string]*TagTotal{}, incomeTags: map[string]*TagTotal{},
	}
	for i := range c.Months {
		c.Months[i].Month = b.period.From.AddMonths(i)
	}
	b.byCurrency[code] = c
	return c
}

// flow adds f to its month, the totals and its category, in the period or
// in the one before; fromInvestments marks the income of the investments.
func (b *reportBuilder) flow(f FlowSum, fromInvestments bool) {
	income := f.Kind == TransactionIncome
	if !income && f.Kind != TransactionExpense {
		return
	}
	inPeriod := b.period.Contains(f.Month)
	if f.Month.IsZero() || (!inPeriod && !b.previous.Contains(f.Month)) {
		return
	}
	c := b.currency(f.Currency)
	totals, categories := &c.Previous, c.expenses
	if inPeriod {
		totals = &c.PeriodTotals
	}
	if income {
		categories = c.income
		totals.Income += f.Amount
	} else {
		totals.Expense += f.Amount
	}
	cat := categories[f.CategoryID]
	if cat == nil {
		cat = &CategoryComparison{CategoryID: f.CategoryID}
		categories[f.CategoryID] = cat
	}
	if !inPeriod {
		cat.PreviousAmount += f.Amount
		return
	}
	cat.Amount += f.Amount
	cat.Count += f.Count
	m := &c.Months[f.Month.Sub(b.period.From)]
	switch {
	case !income:
		m.Expense += f.Amount
	case fromInvestments:
		m.Income += f.Amount
		c.InvestmentIncome += f.Amount
	default:
		m.Income += f.Amount
	}
}

// account adds f to the total of its account.
func (b *reportBuilder) account(f AccountFlowSum) {
	if f.Kind != TransactionIncome && f.Kind != TransactionExpense {
		return
	}
	c := b.currency(f.Currency)
	a := c.accounts[f.AccountID]
	if a == nil {
		a = &AccountTotal{AccountID: f.AccountID}
		c.accounts[f.AccountID] = a
	}
	if f.Kind == TransactionIncome {
		a.Income += f.Amount
	} else {
		a.Expense += f.Amount
	}
	a.Count += f.Count
}

// tag adds f to the total of its tag.
func (b *reportBuilder) tag(f TagFlowSum) {
	if f.Kind != TransactionIncome && f.Kind != TransactionExpense {
		return
	}
	c := b.currency(f.Currency)
	tags := c.expenseTags
	if f.Kind == TransactionIncome {
		tags = c.incomeTags
	}
	t := tags[f.Tag]
	if t == nil {
		t = &TagTotal{Tag: f.Tag}
		tags[f.Tag] = t
	}
	t.Amount += f.Amount
	t.Count += f.Count
}

// sortedKeys lists the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// sortedCategories lists the largest amounts first, then by category id.
func sortedCategories(m map[string]*CategoryComparison) []CategoryComparison {
	out := make([]CategoryComparison, 0, len(m))
	for _, c := range m {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b CategoryComparison) int {
		if c := cmp.Compare(b.Amount, a.Amount); c != 0 {
			return c
		}
		return cmp.Compare(a.CategoryID, b.CategoryID)
	})
	return out
}

// sortedAccounts lists the accounts that moved the most first (expenses
// plus income), then by account id.
func sortedAccounts(m map[string]*AccountTotal) []AccountTotal {
	out := make([]AccountTotal, 0, len(m))
	for _, a := range m {
		out = append(out, *a)
	}
	slices.SortFunc(out, func(a, b AccountTotal) int {
		if c := cmp.Compare(b.Income+b.Expense, a.Income+a.Expense); c != 0 {
			return c
		}
		return cmp.Compare(a.AccountID, b.AccountID)
	})
	return out
}

// sortedTags lists the largest amounts first, then by tag.
func sortedTags(m map[string]*TagTotal) []TagTotal {
	out := make([]TagTotal, 0, len(m))
	for _, t := range m {
		out = append(out, *t)
	}
	slices.SortFunc(out, func(a, b TagTotal) int {
		if c := cmp.Compare(b.Amount, a.Amount); c != 0 {
			return c
		}
		return cmp.Compare(a.Tag, b.Tag)
	})
	return out
}
