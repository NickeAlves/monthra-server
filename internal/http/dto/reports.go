package dto

import (
	"net/url"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Report is the body of GET /reports/summary: a period of whole months by
// currency, with no conversion between them, beside the period of the same
// length before it. Amounts are integers in minor units (cents) of their
// currency. Accounts and categories come as ids: their names, icons and
// colors come from /accounts and /categories.
type Report struct {
	// From and To are the first and last months, "YYYY-MM".
	From string `json:"from"`
	To   string `json:"to"`
	// AsOf is the day the net worth counts up to: To's last day, or the
	// reader's today when that comes first.
	AsOf       string           `json:"asOf"`
	Previous   ReportPeriod     `json:"previous"`
	Currencies []CurrencyReport `json:"currencies"`
}

// ReportPeriod is a run of months, both included.
type ReportPeriod struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// CurrencyReport is the part of a report in one currency. The flows count
// the whole months (days still to come included), as the dashboard does;
// transfers are none, and the investments' income is income.
type CurrencyReport struct {
	Currency string `json:"currency"`
	PeriodTotals
	// InvestmentIncome is the part of Income the investments paid; it is
	// in Months and IncomeByCategory, in no account and no tag.
	InvestmentIncome   int64                `json:"investmentIncome"`
	Previous           PeriodTotals         `json:"previous"`
	Months             []ReportMonth        `json:"months"`
	ExpensesByCategory []CategoryComparison `json:"expensesByCategory"`
	IncomeByCategory   []CategoryComparison `json:"incomeByCategory"`
	ByAccount          []AccountTotal       `json:"byAccount"`
	ExpensesByTag      []TagTotal           `json:"expensesByTag"`
	IncomeByTag        []TagTotal           `json:"incomeByTag"`
}

// PeriodTotals adds up a period. SavingsRateBps is net over income in
// basis points (1234 is 12.34%), negative when more went out than came in,
// null without income.
type PeriodTotals struct {
	Income         int64  `json:"income"`
	Expense        int64  `json:"expense"`
	Net            int64  `json:"net"`
	SavingsRateBps *int64 `json:"savingsRateBps"`
}

// ReportMonth is one month of a report; NetWorth is what the accounts held
// at its end (on AsOf in its month), null for a month after AsOf.
type ReportMonth struct {
	Month    string `json:"month"`
	Income   int64  `json:"income"`
	Expense  int64  `json:"expense"`
	Net      int64  `json:"net"`
	NetWorth *int64 `json:"netWorth"`
}

// CategoryComparison is what one category added up to in the period and in
// the period before; the largest first, then those of the period before
// only (amount 0).
type CategoryComparison struct {
	CategoryID     string `json:"categoryId"`
	Amount         int64  `json:"amount"`
	Count          int    `json:"count"`
	PreviousAmount int64  `json:"previousAmount"`
}

// AccountTotal is the income and expenses of one account in the period
// (archived accounts included), the one that moved the most first.
type AccountTotal struct {
	AccountID string `json:"accountId"`
	Income    int64  `json:"income"`
	Expense   int64  `json:"expense"`
	Count     int    `json:"count"`
}

// TagTotal is what one tag added up to; a transaction with several tags
// counts under each of them.
type TagTotal struct {
	Tag    string `json:"tag"`
	Amount int64  `json:"amount"`
	Count  int    `json:"count"`
}

// NewReport maps a report; empty lists are [], never null.
func NewReport(r domain.Report) Report {
	out := Report{
		From: r.Period.From.String(), To: r.Period.To.String(), AsOf: r.AsOf.String(),
		Previous:   ReportPeriod{From: r.Previous.From.String(), To: r.Previous.To.String()},
		Currencies: make([]CurrencyReport, len(r.Currencies)),
	}
	for i, c := range r.Currencies {
		cr := CurrencyReport{
			Currency: c.Currency, PeriodTotals: newPeriodTotals(c.PeriodTotals), InvestmentIncome: c.InvestmentIncome,
			Previous: newPeriodTotals(c.Previous), Months: make([]ReportMonth, len(c.Months)),
			ExpensesByCategory: newCategoryComparisons(c.ExpensesByCategory), IncomeByCategory: newCategoryComparisons(c.IncomeByCategory),
			ByAccount:     make([]AccountTotal, len(c.ByAccount)),
			ExpensesByTag: newTagTotals(c.ExpensesByTag), IncomeByTag: newTagTotals(c.IncomeByTag),
		}
		for j, m := range c.Months {
			cr.Months[j] = ReportMonth{Month: m.Month.String(), Income: m.Income, Expense: m.Expense, Net: m.Net, NetWorth: m.NetWorth}
		}
		for j, a := range c.ByAccount {
			cr.ByAccount[j] = AccountTotal{AccountID: a.AccountID, Income: a.Income, Expense: a.Expense, Count: a.Count}
		}
		out.Currencies[i] = cr
	}
	return out
}

func newPeriodTotals(t domain.PeriodTotals) PeriodTotals {
	return PeriodTotals{Income: t.Income, Expense: t.Expense, Net: t.Net, SavingsRateBps: t.SavingsRateBps}
}

func newCategoryComparisons(list []domain.CategoryComparison) []CategoryComparison {
	out := make([]CategoryComparison, len(list))
	for i, c := range list {
		out[i] = CategoryComparison{CategoryID: c.CategoryID, Amount: c.Amount, Count: c.Count, PreviousAmount: c.PreviousAmount}
	}
	return out
}

func newTagTotals(list []domain.TagTotal) []TagTotal {
	out := make([]TagTotal, len(list))
	for i, t := range list {
		out[i] = TagTotal{Tag: t.Tag, Amount: t.Amount, Count: t.Count}
	}
	return out
}

// ReportQuery is a parsed GET /reports/summary query; zero fields were not
// given.
type ReportQuery struct {
	From, To domain.Month
	AsOf     domain.Date
}

// ParseReportQuery reads ?from=YYYY-MM&to=YYYY-MM&asOf=YYYY-MM-DD, or
// returns a *domain.ValidationError naming each invalid parameter. The
// bounds of the period are the use case's.
func ParseReportQuery(q url.Values) (ReportQuery, error) {
	var v domain.Validation
	var out ReportQuery
	var err error
	if raw := q.Get("from"); raw != "" {
		out.From, err = domain.ParseMonth(raw)
		v.Check("from", err)
	}
	if raw := q.Get("to"); raw != "" {
		out.To, err = domain.ParseMonth(raw)
		v.Check("to", err)
	}
	if raw := q.Get("asOf"); raw != "" {
		out.AsOf, err = domain.ParseDate(raw)
		v.Check("asOf", err)
	}
	if err := v.Err(); err != nil {
		return ReportQuery{}, err
	}
	return out, nil
}

// ExportQuery is a parsed GET /reports/export query.
type ExportQuery struct {
	// Format is the one format served, "csv".
	Format string
	Filter domain.TransactionFilter
	// AsOf is the reader's today; zero when the query names none.
	AsOf domain.Date
}

// ParseExportQuery reads ?format=csv, the filters of GET /transactions and
// asOf, or returns a *domain.ValidationError naming each invalid parameter.
// The format is required: PDFs are rendered by the app from the report.
func ParseExportQuery(q url.Values) (ExportQuery, error) {
	var v domain.Validation
	out := ExportQuery{Format: q.Get("format"), Filter: parseTransactionFilter(&v, q)}
	switch out.Format {
	case "csv":
	case "":
		v.Add("format", domain.CodeRequired)
	default:
		v.Add("format", domain.CodeInvalid)
	}
	if raw := q.Get("asOf"); raw != "" {
		var err error
		out.AsOf, err = domain.ParseDate(raw)
		v.Check("asOf", err)
	}
	if err := v.Err(); err != nil {
		return ExportQuery{}, err
	}
	return out, nil
}
