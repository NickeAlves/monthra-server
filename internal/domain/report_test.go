package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReportPeriod(t *testing.T) {
	today := day(t, "2026-11-08")
	p, err := ReportPeriod(Month{}, Month{}, today)
	require.NoError(t, err)
	assert.Equal(t, Period{From: month(t, "2025-12"), To: nov2026}, p, "the 12 months up to today's")
	assert.Equal(t, 12, p.Months())
	assert.Equal(t, Period{From: month(t, "2024-12"), To: month(t, "2025-11")}, p.Previous())

	p, err = ReportPeriod(month(t, "2026-01"), Month{}, today)
	require.NoError(t, err)
	assert.Equal(t, Period{From: month(t, "2026-01"), To: nov2026}, p)

	p, err = ReportPeriod(month(t, "2027-03"), Month{}, today)
	require.NoError(t, err)
	assert.Equal(t, Period{From: month(t, "2027-03"), To: month(t, "2027-03")}, p, "a start after today's month ends on itself")

	p, err = ReportPeriod(Month{}, month(t, "2026-03"), today)
	require.NoError(t, err)
	assert.Equal(t, Period{From: month(t, "2025-04"), To: month(t, "2026-03")}, p)

	p, err = ReportPeriod(month(t, "2021-12"), nov2026, today)
	require.NoError(t, err, "60 months")
	assert.Equal(t, ReportMaxMonths, p.Months())

	_, err = ReportPeriod(month(t, "2021-11"), nov2026, today)
	assert.Equal(t, CodeOutOfRange, fieldCode(t, err, "to"), "61 months")
	_, err = ReportPeriod(nov2026, oct2026, today)
	assert.Equal(t, CodeInvalid, fieldCode(t, err, "to"))
}

func TestPeriodContains(t *testing.T) {
	p := Period{From: month(t, "2026-09"), To: nov2026}
	assert.True(t, p.Contains(month(t, "2026-09")))
	assert.True(t, p.Contains(nov2026))
	assert.False(t, p.Contains(month(t, "2026-08")))
	assert.False(t, p.Contains(month(t, "2026-12")))
}

// fieldCode returns the code of field in validation error err.
func fieldCode(t *testing.T, err error, field string) string {
	t.Helper()
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	require.Len(t, ve.Fields, 1)
	assert.Equal(t, field, ve.Fields[0].Field)
	return ve.Fields[0].Code
}

// reportInput is September to December 2026, read on 2026-11-08, over the
// accounts of the dashboard tests.
func reportInput(t *testing.T) ReportInput {
	t.Helper()
	sep, dec := month(t, "2026-09"), month(t, "2026-12")
	brl := func(m Month, kind TransactionKind, category string, amount int64, count int) FlowSum {
		return FlowSum{Month: m, Currency: "BRL", Kind: kind, CategoryID: category, Amount: amount, Count: count}
	}
	return ReportInput{
		Period:   Period{From: sep, To: dec},
		Today:    day(t, "2026-11-08"),
		Accounts: dashboardAccounts(),
		Flows: []FlowSum{
			brl(sep, TransactionExpense, "food", 30000, 3),
			brl(oct2026, TransactionExpense, "rent", 250000, 1),
			brl(nov2026, TransactionExpense, "food", 10000, 1),
			brl(sep, TransactionIncome, "salary", 850000, 1),
			// An installment to come counts in its month, as on the dashboard.
			brl(dec, TransactionExpense, "food", 5000, 1),
			{Month: oct2026, Currency: "USD", Kind: TransactionIncome, CategoryID: "freelance", Amount: 20000, Count: 1},
			// The period before: May to August.
			brl(month(t, "2026-08"), TransactionExpense, "food", 20000, 2),
			brl(month(t, "2026-07"), TransactionExpense, "travel", 70000, 1),
			brl(month(t, "2026-06"), TransactionIncome, "salary", 800000, 1),
			// Neither period, nor a flow.
			brl(month(t, "2026-04"), TransactionExpense, "food", 99999, 1),
			brl(oct2026, TransactionTransfer, "", 99999, 1),
		},
		Income: []FlowSum{
			brl(oct2026, TransactionIncome, "dividends", 1500, 1),
			brl(month(t, "2026-07"), TransactionIncome, "dividends", 500, 1),
		},
		ByAccount: []AccountFlowSum{
			{Currency: "BRL", Kind: TransactionExpense, AccountID: "card", Amount: 15000, Count: 2},
			{Currency: "BRL", Kind: TransactionExpense, AccountID: "bank", Amount: 280000, Count: 4},
			{Currency: "BRL", Kind: TransactionIncome, AccountID: "bank", Amount: 850000, Count: 1},
			{Currency: "USD", Kind: TransactionIncome, AccountID: "usd", Amount: 20000, Count: 1},
			{Currency: "BRL", Kind: TransactionTransfer, AccountID: "old", Amount: 1, Count: 1},
		},
		ByTag: []TagFlowSum{
			{Currency: "BRL", Kind: TransactionExpense, Tag: "trip", Amount: 30000, Count: 2},
			{Currency: "BRL", Kind: TransactionExpense, Tag: "home", Amount: 250000, Count: 1},
			{Currency: "BRL", Kind: TransactionIncome, Tag: "work", Amount: 850000, Count: 1},
			{Currency: "BRL", Kind: TransactionTransfer, Tag: "moving", Amount: 1, Count: 1},
		},
		Legs: []LegSum{
			{AccountID: "bank", Sum: -5000},
			{AccountID: "old", Sum: -2000},
			{AccountID: "bank", Month: sep, Sum: 400000},
			{AccountID: "bank", Month: oct2026, Sum: -100000},
			{AccountID: "card", Month: nov2026, Sum: -30000},
			{AccountID: "usd", Month: oct2026, Sum: 20000},
			{AccountID: "gone", Month: nov2026, Sum: 999},
		},
	}
}

func TestBuildReport(t *testing.T) {
	in := reportInput(t)
	r := BuildReport(in)

	assert.Equal(t, in.Period, r.Period)
	assert.Equal(t, Period{From: month(t, "2026-05"), To: month(t, "2026-08")}, r.Previous)
	assert.Equal(t, "2026-11-08", r.AsOf.String())
	require.Len(t, r.Currencies, 2)

	brl := r.Currencies[0]
	assert.Equal(t, "BRL", brl.Currency)
	assert.Equal(t, PeriodTotals{Income: 851500, Expense: 295000, Net: 556500, SavingsRateBps: ptr(int64(6536))}, brl.PeriodTotals,
		"the investments' income included")
	assert.Equal(t, int64(1500), brl.InvestmentIncome)
	assert.Equal(t, PeriodTotals{Income: 800500, Expense: 90000, Net: 710500, SavingsRateBps: ptr(int64(8876))}, brl.Previous)
	assert.Equal(t, []ReportMonth{
		{Month: month(t, "2026-09"), Income: 850000, Expense: 30000, Net: 820000, NetWorth: ptr(int64(495000))},
		{Month: oct2026, Income: 1500, Expense: 250000, Net: -248500, NetWorth: ptr(int64(395000))},
		{Month: nov2026, Expense: 10000, Net: -10000, NetWorth: ptr(int64(365000))},
		{Month: month(t, "2026-12"), Expense: 5000, Net: -5000},
	}, brl.Months, "the net worth stops at the cut")
	assert.Equal(t, []CategoryComparison{
		{CategoryID: "rent", Amount: 250000, Count: 1},
		{CategoryID: "food", Amount: 45000, Count: 5, PreviousAmount: 20000},
		{CategoryID: "travel", PreviousAmount: 70000},
	}, brl.ExpensesByCategory, "a category of the period before only comes last")
	assert.Equal(t, []CategoryComparison{
		{CategoryID: "salary", Amount: 850000, Count: 1, PreviousAmount: 800000},
		{CategoryID: "dividends", Amount: 1500, Count: 1, PreviousAmount: 500},
	}, brl.IncomeByCategory)
	assert.Equal(t, []AccountTotal{
		{AccountID: "bank", Income: 850000, Expense: 280000, Count: 5},
		{AccountID: "card", Expense: 15000, Count: 2},
	}, brl.ByAccount, "transfers are no flow; the investments are no account")
	assert.Equal(t, []TagTotal{{Tag: "home", Amount: 250000, Count: 1}, {Tag: "trip", Amount: 30000, Count: 2}}, brl.ExpensesByTag)
	assert.Equal(t, []TagTotal{{Tag: "work", Amount: 850000, Count: 1}}, brl.IncomeByTag)

	usd := r.Currencies[1]
	assert.Equal(t, "USD", usd.Currency)
	assert.Equal(t, PeriodTotals{Income: 20000, Net: 20000, SavingsRateBps: ptr(int64(10000))}, usd.PeriodTotals)
	assert.Equal(t, PeriodTotals{}, usd.Previous, "no income: no savings rate")
	assert.Equal(t, []*int64{ptr(int64(5000)), ptr(int64(25000)), ptr(int64(25000)), nil}, netWorths(usd.Months))
	assert.Equal(t, []CategoryComparison{}, usd.ExpensesByCategory)
	assert.Equal(t, []CategoryComparison{{CategoryID: "freelance", Amount: 20000, Count: 1}}, usd.IncomeByCategory)
	assert.Equal(t, []AccountTotal{{AccountID: "usd", Income: 20000, Count: 1}}, usd.ByAccount)
	assert.Equal(t, []TagTotal{}, usd.ExpensesByTag)
	assert.Equal(t, []TagTotal{}, usd.IncomeByTag)
}

func netWorths(months []ReportMonth) []*int64 {
	out := make([]*int64, len(months))
	for i, m := range months {
		out[i] = m.NetWorth
	}
	return out
}

// The net worth of a report is the dashboard's over the same months.
func TestBuildReportNetWorthMatchesTheDashboard(t *testing.T) {
	today := day(t, "2026-11-08")
	first := nov2026.AddMonths(1 - SummaryHistoryMonths)
	legs := []LegSum{
		{AccountID: "bank", Sum: -5000},
		{AccountID: "bank", Month: first.AddMonths(-3), Sum: 7000},
		{AccountID: "bank", Month: first, Sum: 1000},
		{AccountID: "card", Month: month(t, "2026-03"), Sum: -40000},
		{AccountID: "usd", Month: oct2026, Sum: 20000},
		{AccountID: "bank", Month: nov2026, Sum: 600000},
	}
	// The dashboard sums the legs before its history in one; the report the
	// ones before its period.
	dashboardLegs := []LegSum{{AccountID: "bank", Sum: 2000}}
	dashboardLegs = append(dashboardLegs, legs[2:]...)
	s := BuildSummary(nov2026, today, dashboardAccounts(), nil, dashboardLegs, nil)
	r := BuildReport(ReportInput{Period: Period{From: first, To: nov2026}, Today: today, Accounts: dashboardAccounts(), Legs: legs})

	require.Len(t, r.Currencies, len(s.Currencies))
	for i, c := range s.Currencies {
		require.Equal(t, c.Currency, r.Currencies[i].Currency)
		for j, p := range c.History {
			assert.Equal(t, p.NetWorth, r.Currencies[i].Months[j].NetWorth, "%s %s", c.Currency, p.Month)
		}
	}
}

func TestBuildReportEdges(t *testing.T) {
	today := day(t, "2026-11-08")
	p := Period{From: oct2026, To: nov2026}

	r := BuildReport(ReportInput{Period: p, Today: today})
	assert.Equal(t, []CurrencyReport{}, r.Currencies, "nothing to report")

	// Income from an investment in a currency with no account: no balance
	// moved, so its net worth is zero.
	r = BuildReport(ReportInput{Period: p, Today: today, Income: []FlowSum{
		{Month: nov2026, Currency: "EUR", Kind: TransactionIncome, CategoryID: "dividends", Amount: 300, Count: 1},
	}})
	require.Len(t, r.Currencies, 1)
	eur := r.Currencies[0]
	assert.Equal(t, int64(300), eur.InvestmentIncome)
	assert.Equal(t, []*int64{ptr(int64(0)), ptr(int64(0))}, netWorths(eur.Months))
	assert.Equal(t, []AccountTotal{}, eur.ByAccount)

	// A period wholly to come: no net worth yet; a past one ends on its
	// last day.
	r = BuildReport(ReportInput{Period: Period{From: month(t, "2027-01"), To: month(t, "2027-02")}, Today: today, Accounts: dashboardAccounts()})
	assert.Equal(t, "2026-11-08", r.AsOf.String())
	assert.Equal(t, []*int64{nil, nil}, netWorths(r.Currencies[0].Months))
	r = BuildReport(ReportInput{Period: Period{From: month(t, "2025-01"), To: month(t, "2025-02")}, Today: today})
	assert.Equal(t, "2025-02-28", r.AsOf.String())
}

func TestPeriodTotalsSavingsRate(t *testing.T) {
	for _, tc := range []struct {
		income, expense int64
		want            *int64
	}{
		{income: 3, expense: 2, want: ptr(int64(3333))},
		{income: 3, expense: 1, want: ptr(int64(6667))},
		{income: 3, expense: 5, want: ptr(int64(-6667))},
		{income: 1000, expense: 3000, want: ptr(int64(-20000))},
		{income: 1000, expense: 0, want: ptr(int64(10000))},
		{income: 0, expense: 500},
		{income: 1, expense: MaxAmount, want: ptr(-MaxAmount)},
	} {
		totals := PeriodTotals{Income: tc.income, Expense: tc.expense}
		totals.close()
		assert.Equal(t, tc.income-tc.expense, totals.Net)
		assert.Equal(t, tc.want, totals.SavingsRateBps, "%d in, %d out", tc.income, tc.expense)
	}
}

func TestPeriodPrevious(t *testing.T) {
	p := Period{From: NewMonth(2026, time.January), To: NewMonth(2026, time.January)}
	assert.Equal(t, Period{From: NewMonth(2025, time.December), To: NewMonth(2025, time.December)}, p.Previous())
}
