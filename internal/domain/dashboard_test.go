package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	nov2026 = NewMonth(2026, time.November)
	oct2026 = NewMonth(2026, time.October)
)

func TestSummaryCut(t *testing.T) {
	today := day(t, "2026-11-08")
	assert.Equal(t, "2026-11-08", SummaryCut(nov2026, today).String(), "the month running")
	assert.Equal(t, "2026-10-31", SummaryCut(oct2026, today).String(), "a past month")
	assert.Equal(t, "2026-11-08", SummaryCut(NewMonth(2027, time.March), today).String(), "a month to come")
	assert.Equal(t, "2026-11-30", SummaryCut(nov2026, day(t, "2026-11-30")).String())
}

// dashboardAccounts are a bank, a card and an archived wallet in BRL, and
// a USD account.
func dashboardAccounts() []Account {
	archived := txNow
	return []Account{
		{ID: "bank", Currency: "BRL", InitialBalance: 100000},
		{ID: "card", Type: AccountCreditCard, Currency: "BRL"},
		{ID: "usd", Currency: "USD", InitialBalance: 5000},
		{ID: "old", Currency: "BRL", InitialBalance: 2000, ArchivedAt: &archived},
	}
}

func scheduledTx(t *testing.T, kind TransactionKind, currency string, amount int64, date string) Transaction {
	t.Helper()
	return Transaction{Kind: kind, Currency: currency, Amount: amount, Date: day(t, date)}
}

func TestBuildSummary(t *testing.T) {
	flows := []FlowSum{
		{Month: nov2026, Currency: "BRL", Kind: TransactionExpense, CategoryID: "food", Amount: 30000, Count: 3},
		{Month: nov2026, Currency: "BRL", Kind: TransactionExpense, CategoryID: "rent", Amount: 250000, Count: 1},
		{Month: nov2026, Currency: "BRL", Kind: TransactionIncome, CategoryID: "salary", Amount: 850000, Count: 1},
		{Month: oct2026, Currency: "BRL", Kind: TransactionExpense, CategoryID: "food", Amount: 10000, Count: 1},
		{Month: nov2026, Currency: "USD", Kind: TransactionIncome, CategoryID: "freelance", Amount: 20000, Count: 1},
		{Month: NewMonth(2025, time.November), Currency: "BRL", Kind: TransactionExpense, CategoryID: "food", Amount: 1, Count: 1},
	}
	legs := []LegSum{
		{AccountID: "bank", Sum: -5000},
		{AccountID: "old", Sum: -2000},
		{AccountID: "bank", Month: oct2026, Sum: -10000},
		{AccountID: "bank", Month: nov2026, Sum: 600000},
		{AccountID: "card", Month: nov2026, Sum: -30000},
		{AccountID: "usd", Month: nov2026, Sum: 20000},
		{AccountID: "gone", Month: nov2026, Sum: 999},
	}
	scheduled := []Transaction{
		scheduledTx(t, TransactionExpense, "BRL", 4000, "2026-11-20"),
		scheduledTx(t, TransactionTransfer, "BRL", 50000, "2026-11-15"),
		scheduledTx(t, TransactionIncome, "USD", 1000, "2026-11-30"),
		scheduledTx(t, TransactionExpense, "BRL", 7000, "2026-12-05"),
	}
	s := BuildSummary(nov2026, day(t, "2026-11-08"), dashboardAccounts(), flows, legs, scheduled)

	assert.Equal(t, nov2026, s.Month)
	assert.Equal(t, "2026-11-08", s.AsOf.String())
	assert.Equal(t, []AccountBalance{
		{AccountID: "bank", Currency: "BRL", Balance: 685000},
		{AccountID: "card", Currency: "BRL", Balance: -30000},
		{AccountID: "usd", Currency: "USD", Balance: 25000},
		{AccountID: "old", Currency: "BRL", Balance: 0},
	}, s.Accounts, "an unknown account is left out")
	require.Len(t, s.Currencies, 2)

	brl := s.Currencies[0]
	assert.Equal(t, "BRL", brl.Currency)
	assert.Equal(t, int64(850000), brl.Income)
	assert.Equal(t, int64(280000), brl.Expense)
	assert.Equal(t, int64(570000), brl.Net)
	assert.Equal(t, Flows{Expense: 4000}, brl.Scheduled, "no transfer, nothing from December")
	assert.Equal(t, int64(655000), brl.Balance, "the card's debt counts against it")
	assert.Equal(t, []CategoryTotal{{CategoryID: "rent", Amount: 250000, Count: 1}, {CategoryID: "food", Amount: 30000, Count: 3}}, brl.ExpensesByCategory)
	assert.Equal(t, []CategoryTotal{{CategoryID: "salary", Amount: 850000, Count: 1}}, brl.IncomeByCategory)
	require.Len(t, brl.History, SummaryHistoryMonths)
	assert.Equal(t, "2025-12", brl.History[0].Month.String())
	assert.Equal(t, "2026-11", brl.History[11].Month.String())
	for _, p := range brl.History[:10] {
		require.NotNil(t, p.NetWorth, p.Month.String())
		assert.Equal(t, int64(95000), *p.NetWorth, "the initial balances and what came before: %s", p.Month)
		assert.Zero(t, p.Income+p.Expense, "December 2025 on")
	}
	assert.Equal(t, int64(10000), brl.History[10].Expense)
	assert.Equal(t, int64(85000), *brl.History[10].NetWorth)
	assert.Equal(t, SummaryPoint{Month: nov2026, Income: 850000, Expense: 280000, NetWorth: ptr(int64(655000))}, brl.History[11])

	usd := s.Currencies[1]
	assert.Equal(t, "USD", usd.Currency)
	assert.Equal(t, int64(20000), usd.Net)
	assert.Equal(t, Flows{Income: 1000}, usd.Scheduled)
	assert.Equal(t, int64(25000), usd.Balance)
	assert.Equal(t, []CategoryTotal{}, usd.ExpensesByCategory)
	assert.Equal(t, int64(5000), *usd.History[0].NetWorth)
}

func TestBuildSummaryMonths(t *testing.T) {
	today := day(t, "2026-11-08")
	legs := []LegSum{{AccountID: "bank", Month: oct2026, Sum: -10000}, {AccountID: "bank", Month: nov2026, Sum: -1000}}
	flows := []FlowSum{{Month: NewMonth(2026, time.December), Currency: "BRL", Kind: TransactionExpense, CategoryID: "food", Amount: 3000, Count: 1}}

	// A month to come: what is already registered (an installment), no
	// net worth after today.
	next := BuildSummary(NewMonth(2026, time.December), today, dashboardAccounts()[:1], flows, legs, nil)
	assert.Equal(t, "2026-11-08", next.AsOf.String())
	brl := next.Currencies[0]
	assert.Equal(t, int64(3000), brl.Expense)
	assert.Nil(t, brl.History[11].NetWorth, "December has not come")
	assert.Equal(t, int64(89000), *brl.History[10].NetWorth)
	assert.Equal(t, int64(89000), brl.Balance)

	// A past month: up to its last day.
	past := BuildSummary(oct2026, today, dashboardAccounts()[:1], nil, legs[:1], nil)
	assert.Equal(t, "2026-10-31", past.AsOf.String())
	assert.Equal(t, "2025-11", past.Currencies[0].History[0].Month.String())
	assert.Equal(t, int64(90000), past.Currencies[0].Balance)
	assert.Equal(t, int64(90000), past.Accounts[0].Balance)

	// Legs after the summary's month never count.
	late := BuildSummary(oct2026, today, dashboardAccounts()[:1], nil, legs, nil)
	assert.Equal(t, int64(90000), late.Accounts[0].Balance)
}

func TestBuildSummaryEdges(t *testing.T) {
	empty := BuildSummary(nov2026, day(t, "2026-11-08"), nil, nil, nil, nil)
	assert.Equal(t, []CurrencySummary{}, empty.Currencies)
	assert.Equal(t, []AccountBalance{}, empty.Accounts)

	// Equal totals sort by category id; a currency with flows and no
	// account still shows.
	flows := []FlowSum{
		{Month: nov2026, Currency: "EUR", Kind: TransactionExpense, CategoryID: "b", Amount: 100, Count: 1},
		{Month: nov2026, Currency: "EUR", Kind: TransactionExpense, CategoryID: "a", Amount: 100, Count: 2},
		{Month: nov2026, Currency: "EUR", Kind: TransactionExpense, CategoryID: "a", Amount: 50, Count: 1},
		{Month: nov2026, Currency: "EUR", Kind: TransactionExpense, CategoryID: "c", Amount: 100, Count: 1},
	}
	s := BuildSummary(nov2026, day(t, "2026-11-08"), nil, flows, nil, nil)
	require.Len(t, s.Currencies, 1)
	assert.Equal(t, []CategoryTotal{
		{CategoryID: "a", Amount: 150, Count: 3},
		{CategoryID: "b", Amount: 100, Count: 1},
		{CategoryID: "c", Amount: 100, Count: 1},
	}, s.Currencies[0].ExpensesByCategory)
	assert.Equal(t, int64(0), *s.Currencies[0].History[11].NetWorth)
}
