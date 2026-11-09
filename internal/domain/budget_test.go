package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	budgetNow   = time.Date(2026, 11, 9, 10, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	budgetMonth = NewMonth(2026, time.November)
)

func TestNewBudget(t *testing.T) {
	b, err := NewBudget("u1", BudgetInput{CategoryID: "c1", Currency: "BRL", Limit: 150000}, budgetMonth, budgetNow)
	require.NoError(t, err)
	assert.Equal(t, "u1", b.UserID)
	assert.Equal(t, "c1", b.CategoryID)
	assert.Equal(t, budgetMonth, b.Month, "an empty month is the reader's")
	assert.Equal(t, "BRL", b.Currency)
	assert.Equal(t, int64(150000), b.Limit)
	assert.Equal(t, DefaultBudgetWarnAt, b.WarnAt)
	assert.Nil(t, b.WarnedAt)
	assert.Nil(t, b.ExceededAt)
	assert.Equal(t, time.UTC, b.CreatedAt.Location())
	assert.Equal(t, b.CreatedAt, b.UpdatedAt)

	b, err = NewBudget("u1", BudgetInput{CategoryID: "c1", Month: "2025-03", Currency: "USD", Limit: 1, WarnAt: ptr(100)}, budgetMonth, budgetNow)
	require.NoError(t, err)
	assert.Equal(t, NewMonth(2025, time.March), b.Month, "a month gone by")
	assert.Equal(t, 100, b.WarnAt)

	_, err = NewBudget("u1", BudgetInput{CategoryID: "c1", Month: "2026-11", Currency: "EUR", Limit: MaxAmount, WarnAt: ptr(1)}, budgetMonth, budgetNow)
	assert.NoError(t, err, "the reader's month, the largest limit, the least share")
}

func TestNewBudgetValidation(t *testing.T) {
	valid := BudgetInput{CategoryID: "c1", Currency: "BRL", Limit: 1000}
	with := func(f func(*BudgetInput)) BudgetInput {
		in := valid
		f(&in)
		return in
	}
	for name, tc := range map[string]struct {
		in   BudgetInput
		want map[string]string
	}{
		"everything missing": {BudgetInput{}, map[string]string{
			"categoryId": CodeRequired, "currency": CodeRequired, "limit": CodeOutOfRange,
		}},
		"zero limit":       {with(func(in *BudgetInput) { in.Limit = 0 }), map[string]string{"limit": CodeOutOfRange}},
		"negative limit":   {with(func(in *BudgetInput) { in.Limit = -1 }), map[string]string{"limit": CodeOutOfRange}},
		"limit too large":  {with(func(in *BudgetInput) { in.Limit = MaxAmount + 1 }), map[string]string{"limit": CodeOutOfRange}},
		"warnAt 0":         {with(func(in *BudgetInput) { in.WarnAt = ptr(0) }), map[string]string{"warnAt": CodeOutOfRange}},
		"warnAt 101":       {with(func(in *BudgetInput) { in.WarnAt = ptr(101) }), map[string]string{"warnAt": CodeOutOfRange}},
		"unknown currency": {with(func(in *BudgetInput) { in.Currency = "GBP" }), map[string]string{"currency": CodeInvalid}},
		"lower case":       {with(func(in *BudgetInput) { in.Currency = "brl" }), map[string]string{"currency": CodeInvalid}},
		"bad month":        {with(func(in *BudgetInput) { in.Month = "2026-13" }), map[string]string{"month": CodeInvalid}},
		"a day":            {with(func(in *BudgetInput) { in.Month = "2026-11-01" }), map[string]string{"month": CodeInvalid}},
		"next month":       {with(func(in *BudgetInput) { in.Month = "2026-12" }), map[string]string{"month": CodeOutOfRange}},
		"before 1900":      {with(func(in *BudgetInput) { in.Month = "1899-12" }), map[string]string{"month": CodeOutOfRange}},
	} {
		_, err := NewBudget("u1", tc.in, budgetMonth, budgetNow)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}
}

func TestBudgetPatch(t *testing.T) {
	later := budgetNow.Add(time.Hour)
	current, err := NewBudget("u1", BudgetInput{CategoryID: "c1", Currency: "BRL", Limit: 1000}, budgetMonth, budgetNow)
	require.NoError(t, err)

	next, changed, err := BudgetPatch{}.Apply(current, later)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, current, next)

	next, changed, err = BudgetPatch{Limit: ptr(int64(1000)), WarnAt: ptr(80)}.Apply(current, later)
	require.NoError(t, err)
	assert.False(t, changed, "the same values change nothing")
	assert.Equal(t, current.UpdatedAt, next.UpdatedAt)

	next, changed, err = BudgetPatch{Limit: ptr(int64(2000)), WarnAt: ptr(90)}.Apply(current, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, int64(2000), next.Limit)
	assert.Equal(t, 90, next.WarnAt)
	assert.Equal(t, later.UTC(), next.UpdatedAt)
	assert.Equal(t, current.CreatedAt, next.CreatedAt)

	_, _, err = BudgetPatch{Limit: ptr(int64(0)), WarnAt: ptr(101)}.Apply(current, later)
	assert.Equal(t, map[string]string{"limit": CodeOutOfRange, "warnAt": CodeOutOfRange}, fieldsOf(t, err))
}

func TestBudgetStatus(t *testing.T) {
	b := Budget{Limit: 1000, WarnAt: 80}
	for spent, want := range map[int64]BudgetStatus{
		0: BudgetOK, 799: BudgetOK, 800: BudgetWarning, 1000: BudgetWarning, 1001: BudgetExceeded,
		-50: BudgetOK, MaxAmount * 1000: BudgetExceeded,
	} {
		assert.Equal(t, want, b.Status(spent), spent)
	}

	// The share is rounded up: 80% of 999 is 799.2, so 799 is still ok.
	b = Budget{Limit: 999, WarnAt: 80}
	assert.Equal(t, BudgetOK, b.Status(799))
	assert.Equal(t, BudgetWarning, b.Status(800))

	// A share of 100% warns only on the limit itself.
	b = Budget{Limit: 1000, WarnAt: 100}
	assert.Equal(t, BudgetOK, b.Status(999))
	assert.Equal(t, BudgetWarning, b.Status(1000))

	// The largest limit and share stay exact.
	b = Budget{Limit: MaxAmount, WarnAt: 100}
	assert.Equal(t, BudgetOK, b.Status(MaxAmount-1))
	assert.Equal(t, BudgetWarning, b.Status(MaxAmount))
}

func TestBudgetRearmedAndDueAlert(t *testing.T) {
	t1, t2 := budgetNow, budgetNow.Add(time.Hour)
	b := Budget{Limit: 1000, WarnAt: 80}

	due, ok := b.DueAlert(500)
	assert.False(t, ok, "ok gives no alert")
	assert.Empty(t, due)
	due, ok = b.DueAlert(800)
	assert.True(t, ok)
	assert.Equal(t, BudgetWarning, due)
	due, ok = b.DueAlert(1500)
	assert.True(t, ok)
	assert.Equal(t, BudgetExceeded, due, "exceeded comes before the warning")

	// Rearmed at the warning marks it, so it gives no alert anymore, but
	// exceeding still does.
	warned := b.Rearmed(800, t1)
	require.NotNil(t, warned.WarnedAt)
	assert.Equal(t, t1.UTC(), *warned.WarnedAt)
	assert.Nil(t, warned.ExceededAt)
	_, ok = warned.DueAlert(900)
	assert.False(t, ok)
	due, ok = warned.DueAlert(1001)
	assert.True(t, ok)
	assert.Equal(t, BudgetExceeded, due)

	// Rearmed past the limit keeps the earlier warning mark.
	exceeded := warned.Rearmed(1200, t2)
	assert.Equal(t, t1.UTC(), *exceeded.WarnedAt)
	assert.Equal(t, t2.UTC(), *exceeded.ExceededAt)
	_, ok = exceeded.DueAlert(5000)
	assert.False(t, ok, "each alert goes out once")
	_, ok = exceeded.DueAlert(100)
	assert.False(t, ok, "spending less rearms nothing")

	// A limit raised rearms what is no longer crossed.
	exceeded.Limit = 10000
	raised := exceeded.Rearmed(1200, t2)
	assert.Nil(t, raised.WarnedAt)
	assert.Nil(t, raised.ExceededAt)
	due, ok = raised.DueAlert(8000)
	assert.True(t, ok)
	assert.Equal(t, BudgetWarning, due)
}

func TestRollBudgets(t *testing.T) {
	marked := budgetNow.UTC()
	from := []Budget{
		{ID: "b1", UserID: "u1", CategoryID: "c1", Month: budgetMonth, Currency: "BRL", Limit: 1000, WarnAt: 80, WarnedAt: &marked, ExceededAt: &marked},
		{ID: "b2", UserID: "u1", CategoryID: "c2", Month: budgetMonth, Currency: "USD", Limit: 50, WarnAt: 100},
	}
	next := budgetMonth.AddMonths(1)
	later := budgetNow.Add(24 * time.Hour)
	got := RollBudgets(from, next, later)
	require.Len(t, got, 2)
	for i, b := range got {
		assert.Empty(t, b.ID, "the store assigns ids")
		assert.Equal(t, next, b.Month)
		assert.Equal(t, from[i].UserID, b.UserID)
		assert.Equal(t, from[i].CategoryID, b.CategoryID)
		assert.Equal(t, from[i].Currency, b.Currency)
		assert.Equal(t, from[i].Limit, b.Limit)
		assert.Equal(t, from[i].WarnAt, b.WarnAt)
		assert.Nil(t, b.WarnedAt, "a new month alerts again")
		assert.Nil(t, b.ExceededAt)
		assert.Equal(t, later.UTC(), b.CreatedAt)
		assert.Equal(t, later.UTC(), b.UpdatedAt)
	}
	assert.Empty(t, RollBudgets(nil, next, later))
}

func TestUseBudgets(t *testing.T) {
	prev := budgetMonth.AddMonths(-1)
	budgets := []Budget{
		{ID: "b1", CategoryID: "food", Currency: "BRL", Limit: 1000, WarnAt: 80},
		{ID: "b2", CategoryID: "food", Currency: "USD", Limit: 100, WarnAt: 80},
		{ID: "b3", CategoryID: "fun", Currency: "BRL", Limit: 500, WarnAt: 80},
	}
	flows := []FlowSum{
		{Month: budgetMonth, Currency: "BRL", Kind: TransactionExpense, CategoryID: "food", Amount: 700, Count: 3},
		{Month: budgetMonth, Currency: "BRL", Kind: TransactionExpense, CategoryID: "food", Amount: 200, Count: 1},
		{Month: budgetMonth, Currency: "USD", Kind: TransactionExpense, CategoryID: "food", Amount: 150, Count: 1},
		{Month: budgetMonth, Currency: "EUR", Kind: TransactionExpense, CategoryID: "food", Amount: 999, Count: 1},
		{Month: prev, Currency: "BRL", Kind: TransactionExpense, CategoryID: "fun", Amount: 999, Count: 1},
		{Month: budgetMonth, Currency: "BRL", Kind: TransactionIncome, CategoryID: "fun", Amount: 999, Count: 1},
	}
	scheduled := []Transaction{
		{Kind: TransactionExpense, CategoryID: "fun", Currency: "BRL", Amount: 300, Date: budgetMonth.Day(20)},
		{Kind: TransactionExpense, CategoryID: "fun", Currency: "BRL", Amount: 300, Date: budgetMonth.AddMonths(1).Day(1)},
		{Kind: TransactionTransfer, Currency: "BRL", Amount: 300, Date: budgetMonth.Day(20)},
		{Kind: TransactionIncome, CategoryID: "fun", Currency: "BRL", Amount: 300, Date: budgetMonth.Day(20)},
	}
	got := UseBudgets(budgets, budgetMonth, flows, scheduled)
	require.Len(t, got, 3)

	assert.Equal(t, "b1", got[0].Budget.ID)
	assert.Equal(t, int64(900), got[0].Spent, "both sums of the category in BRL")
	assert.Equal(t, int64(100), got[0].Remaining())
	assert.Equal(t, BudgetWarning, got[0].Status())

	assert.Equal(t, int64(150), got[1].Spent, "the USD expenses only")
	assert.Equal(t, int64(-50), got[1].Remaining())
	assert.Equal(t, BudgetExceeded, got[1].Status())

	assert.Zero(t, got[2].Spent, "income and other months are left out")
	assert.Equal(t, int64(300), got[2].Scheduled, "the expenses still to come in the month")
	assert.Equal(t, BudgetOK, got[2].Status(), "what is scheduled never counts toward the status")

	totals := TotalBudgets(got)
	assert.Equal(t, []BudgetTotal{
		{Currency: "BRL", Limit: 1500, Spent: 900, Scheduled: 300},
		{Currency: "USD", Limit: 100, Spent: 150},
	}, totals)
	assert.Equal(t, int64(600), totals[0].Remaining())
	assert.Empty(t, TotalBudgets(nil))
}
