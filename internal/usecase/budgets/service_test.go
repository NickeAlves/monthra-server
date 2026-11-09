package budgets

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

type fixture struct {
	svc         *Service
	store       *memStore
	marker      *memMarker
	ledger      *fakeLedger
	recurrences *fakeRecurrences
	now         *time.Time
}

func newFixture() *fixture {
	now := t0
	f := &fixture{
		store: newMemStore(), marker: newMemMarker(), ledger: &fakeLedger{}, recurrences: &fakeRecurrences{}, now: &now,
	}
	categories := fakeCategories{
		"food":   {ID: "food", UserID: "u1", Kind: domain.CategoryExpense},
		"fun":    {ID: "fun", UserID: "u1", Kind: domain.CategoryExpense},
		"salary": {ID: "salary", UserID: "u1", Kind: domain.CategoryIncome},
		"theirs": {ID: "theirs", UserID: "u2", Kind: domain.CategoryExpense},
	}
	f.svc = NewService(f.store, f.marker, categories, f.ledger, f.recurrences, Options{Now: func() time.Time { return *f.now }})
	return f
}

func (f *fixture) create(t *testing.T, in domain.BudgetInput) domain.BudgetUsage {
	t.Helper()
	if in.Currency == "" {
		in.Currency = "BRL"
	}
	u, err := f.svc.Create(context.Background(), "u1", in, domain.Date{})
	require.NoError(t, err)
	return u
}

func TestBudgetLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.ledger.spend(november, "food", 50000)
	f.ledger.spend(november.AddMonths(-1), "food", 99999)
	f.recurrences.scheduled = []domain.Transaction{{Kind: domain.TransactionExpense, CategoryID: "food", Currency: "BRL", Amount: 20000, Date: november.Day(25)}}

	u := f.create(t, domain.BudgetInput{CategoryID: "food", Limit: 100000})
	assert.NotEmpty(t, u.Budget.ID)
	assert.Equal(t, november, u.Budget.Month)
	assert.Equal(t, int64(50000), u.Spent)
	assert.Equal(t, int64(20000), u.Scheduled)
	assert.Equal(t, domain.BudgetOK, u.Status())
	assert.Nil(t, u.Budget.WarnedAt)
	assert.Equal(t, []string{"u1 2026-11-01 2026-11-30"}, f.ledger.calls, "the budget's month")

	month, err := f.svc.List(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, november, month.Month, "the reader's month by default")
	assert.False(t, month.Projected)
	require.Len(t, month.Budgets, 1)
	assert.Equal(t, u, month.Budgets[0])
	assert.Equal(t, []domain.BudgetTotal{{Currency: "BRL", Limit: 100000, Spent: 50000, Scheduled: 20000}}, month.Totals)

	got, err := f.svc.Get(ctx, "u1", u.Budget.ID, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, u, got)

	// Lowering the limit past what was spent marks both alerts as given.
	*f.now = t0.Add(time.Hour)
	updated, err := f.svc.Update(ctx, "u1", u.Budget.ID, domain.BudgetPatch{Limit: ptr(int64(40000))}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, domain.BudgetExceeded, updated.Status())
	require.NotNil(t, updated.Budget.ExceededAt)
	require.NotNil(t, updated.Budget.WarnedAt)
	assert.Equal(t, t0.Add(time.Hour), updated.Budget.UpdatedAt)
	stored, err := f.store.ByID(ctx, "u1", u.Budget.ID)
	require.NoError(t, err)
	assert.Equal(t, updated.Budget, stored)

	// A patch that changes nothing writes nothing.
	*f.now = t0.Add(2 * time.Hour)
	same, err := f.svc.Update(ctx, "u1", u.Budget.ID, domain.BudgetPatch{Limit: ptr(int64(40000))}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, updated, same)

	// Raising it again rearms both.
	raised, err := f.svc.Update(ctx, "u1", u.Budget.ID, domain.BudgetPatch{Limit: ptr(int64(1000000)), WarnAt: ptr(90)}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, domain.BudgetOK, raised.Status())
	assert.Nil(t, raised.Budget.WarnedAt)
	assert.Nil(t, raised.Budget.ExceededAt)
	assert.Equal(t, 90, raised.Budget.WarnAt)

	_, err = f.svc.Update(ctx, "u1", u.Budget.ID, domain.BudgetPatch{WarnAt: ptr(0)}, domain.Date{})
	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)

	require.NoError(t, f.svc.Delete(ctx, "u1", u.Budget.ID))
	assert.ErrorIs(t, f.svc.Delete(ctx, "u1", u.Budget.ID), domain.ErrNotFound)
	month, err = f.svc.List(ctx, "u1", november, domain.Date{})
	require.NoError(t, err)
	assert.Empty(t, month.Budgets)
	assert.NotNil(t, month.Budgets, "an empty list, not null")
	assert.Empty(t, month.Totals)
}

func TestBudgetsMaterializeFirst(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	asOf := day(t, "2026-11-10")
	u, err := f.svc.Create(ctx, "u1", domain.BudgetInput{CategoryID: "food", Currency: "BRL", Limit: 100}, asOf)
	require.NoError(t, err)
	_, err = f.svc.List(ctx, "u1", domain.Month{}, asOf)
	require.NoError(t, err)
	_, err = f.svc.Get(ctx, "u1", u.Budget.ID, asOf)
	require.NoError(t, err)
	_, err = f.svc.Update(ctx, "u1", u.Budget.ID, domain.BudgetPatch{Limit: ptr(int64(200))}, day(t, "2030-01-01"))
	require.NoError(t, err)
	// Every operation that reads spending creates the occurrences first; a
	// later asOf is held within a day of today in UTC.
	assert.Equal(t, []string{"u1 2026-11-10", "u1 2026-11-10", "u1 2026-11-10", "u1 2026-11-10"}, f.recurrences.materialized)
}

func TestCreateValidation(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	field := func(err error) map[string]string {
		var invalid *domain.ValidationError
		require.ErrorAs(t, err, &invalid)
		out := map[string]string{}
		for _, fe := range invalid.Fields {
			out[fe.Field] = fe.Code
		}
		return out
	}
	for name, tc := range map[string]struct {
		in   domain.BudgetInput
		want map[string]string
	}{
		"income category":  {domain.BudgetInput{CategoryID: "salary", Currency: "BRL", Limit: 1}, map[string]string{"categoryId": domain.CodeKindMismatch}},
		"unknown category": {domain.BudgetInput{CategoryID: "nope", Currency: "BRL", Limit: 1}, map[string]string{"categoryId": domain.CodeInvalid}},
		"another user's":   {domain.BudgetInput{CategoryID: "theirs", Currency: "BRL", Limit: 1}, map[string]string{"categoryId": domain.CodeInvalid}},
		"next month":       {domain.BudgetInput{CategoryID: "food", Currency: "BRL", Limit: 1, Month: "2026-12"}, map[string]string{"month": domain.CodeOutOfRange}},
		"bad input":        {domain.BudgetInput{CategoryID: "food"}, map[string]string{"currency": domain.CodeRequired, "limit": domain.CodeOutOfRange}},
	} {
		_, err := f.svc.Create(ctx, "u1", tc.in, domain.Date{})
		assert.Equal(t, tc.want, field(err), name)
	}
	assert.Empty(t, f.recurrences.materialized, "an invalid budget reads nothing")

	// The reader's month follows asOf: on the last UTC day of November, a
	// reader already in December may budget December.
	*f.now = time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC)
	u, err := f.svc.Create(ctx, "u1", domain.BudgetInput{CategoryID: "food", Currency: "BRL", Limit: 1, Month: "2026-12"}, day(t, "2026-12-01"))
	require.NoError(t, err)
	assert.Equal(t, month(t, "2026-12"), u.Budget.Month)

	f.create(t, domain.BudgetInput{CategoryID: "fun", Limit: 1})
	_, err = f.svc.Create(ctx, "u1", domain.BudgetInput{CategoryID: "fun", Currency: "USD", Limit: 5}, domain.Date{})
	assert.ErrorIs(t, err, domain.ErrBudgetExists, "one budget per category and month, whatever the currency")
}

func TestCreateIsSilent(t *testing.T) {
	f := newFixture()
	f.ledger.spend(november, "food", 900)
	u := f.create(t, domain.BudgetInput{CategoryID: "food", Limit: 1000})
	assert.Equal(t, domain.BudgetWarning, u.Status())
	require.NotNil(t, u.Budget.WarnedAt, "already past the warning: no alert for it")
	assert.Nil(t, u.Budget.ExceededAt, "exceeding still alerts")
	stored, err := f.store.ByID(context.Background(), "u1", u.Budget.ID)
	require.NoError(t, err)
	assert.Equal(t, u.Budget, stored)
}

func TestBudgetsOfAnotherUser(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	u := f.create(t, domain.BudgetInput{CategoryID: "food", Limit: 1000})
	_, err := f.svc.Get(ctx, "u2", u.Budget.ID, domain.Date{})
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = f.svc.Update(ctx, "u2", u.Budget.ID, domain.BudgetPatch{Limit: ptr(int64(1))}, domain.Date{})
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.ErrorIs(t, f.svc.Delete(ctx, "u2", u.Budget.ID), domain.ErrNotFound)
	month, err := f.svc.List(ctx, "u2", november, domain.Date{})
	require.NoError(t, err)
	assert.Empty(t, month.Budgets)
}

func TestBudgetsCarryOver(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	*f.now = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	food := f.create(t, domain.BudgetInput{CategoryID: "food", Limit: 1000, WarnAt: ptr(70)})
	f.create(t, domain.BudgetInput{CategoryID: "fun", Limit: 500})
	// The alert marks stay with October.
	_, err := f.store.ClaimAlert(ctx, "u1", food.Budget.ID, domain.BudgetExceeded, *f.now)
	require.NoError(t, err)

	// November starts: it gets October's budgets, with new ids and no marks.
	*f.now = t0
	nov, err := f.svc.List(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	require.Len(t, nov.Budgets, 2)
	got := nov.Budgets[0].Budget
	assert.NotEqual(t, food.Budget.ID, got.ID)
	assert.Equal(t, november, got.Month)
	assert.Equal(t, "food", got.CategoryID)
	assert.Equal(t, int64(1000), got.Limit)
	assert.Equal(t, 70, got.WarnAt)
	assert.Nil(t, got.WarnedAt)
	assert.Nil(t, got.ExceededAt)
	assert.Equal(t, "fun", nov.Budgets[1].Budget.CategoryID)

	// Changing November leaves October as it was, and carries on.
	_, err = f.svc.Update(ctx, "u1", got.ID, domain.BudgetPatch{Limit: ptr(int64(3000))}, domain.Date{})
	require.NoError(t, err)
	require.NoError(t, f.svc.Delete(ctx, "u1", nov.Budgets[1].Budget.ID))
	oct, err := f.svc.List(ctx, "u1", month(t, "2026-10"), domain.Date{})
	require.NoError(t, err)
	require.Len(t, oct.Budgets, 2, "a month gone by keeps its budgets")
	assert.Equal(t, int64(1000), oct.Budgets[0].Budget.Limit)

	// Three months later: December, January and February are carried
	// over, each from the one before.
	*f.now = time.Date(2027, 2, 3, 12, 0, 0, 0, time.UTC)
	feb, err := f.svc.List(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	require.Len(t, feb.Budgets, 1, "the budget deleted in November is gone from then on")
	assert.Equal(t, int64(3000), feb.Budgets[0].Budget.Limit, "the change applies from its month on")
	assert.Equal(t, []string{"2026-10", "2026-11", "2026-12", "2027-01", "2027-02"}, f.store.months("u1"))
	assert.Equal(t, month(t, "2027-02"), f.marker.through["u1"])

	// Once done, a month is not carried over again: a deletion stays.
	require.NoError(t, f.svc.Delete(ctx, "u1", feb.Budgets[0].Budget.ID))
	feb, err = f.svc.List(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	assert.Empty(t, feb.Budgets)
}

func TestBudgetsCarryOverAfterALongGap(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	*f.now = time.Date(2025, 1, 10, 12, 0, 0, 0, time.UTC)
	f.create(t, domain.BudgetInput{CategoryID: "food", Limit: 1000})

	// 17 months later only the last 12 are filled, from January 2025.
	*f.now = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	jun, err := f.svc.List(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	require.Len(t, jun.Budgets, 1)
	months := f.store.months("u1")
	assert.Len(t, months, 13)
	assert.Equal(t, "2025-01", months[0])
	assert.Equal(t, "2025-07", months[1], "the months before the window stay empty")
	assert.Equal(t, "2026-06", months[12])
}

func TestBudgetsFirstCarryOver(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	// A user who never had budgets gets the window checked once.
	_, err := f.svc.List(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, november, f.marker.through["u1"])
	assert.Equal(t, domain.BudgetRollWindow+1, f.store.lists, "12 months to fill, then the month read")
	_, err = f.svc.List(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, domain.BudgetRollWindow+2, f.store.lists, "then only the month read")
	assert.Equal(t, 1, f.marker.marks)

	// A user that is gone counts as done.
	f.marker.through["gone"] = domain.MaxMonth
	_, err = f.svc.List(ctx, "gone", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, 1, f.marker.marks)
}

func TestBudgetsCarryOverFailures(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	*f.now = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	f.create(t, domain.BudgetInput{CategoryID: "food", Limit: 1000})
	*f.now = t0

	f.store.rollErr = errBoom
	_, err := f.svc.List(ctx, "u1", domain.Month{}, domain.Date{})
	require.ErrorIs(t, err, errBoom)
	assert.Equal(t, month(t, "2026-10"), f.marker.through["u1"], "not marked: the next read completes it")

	f.store.rollErr = nil
	nov, err := f.svc.List(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	assert.Len(t, nov.Budgets, 1)

	f.marker.err = errBoom
	_, err = f.svc.List(ctx, "u1", domain.Month{}, domain.Date{})
	assert.ErrorIs(t, err, errBoom)
	_, err = f.svc.Create(ctx, "u1", domain.BudgetInput{CategoryID: "fun", Currency: "BRL", Limit: 1}, domain.Date{})
	assert.ErrorIs(t, err, errBoom)
	f.marker.err = nil

	f.store.listErr = errBoom
	*f.now = time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	_, err = f.svc.List(ctx, "u1", domain.Month{}, domain.Date{})
	assert.ErrorIs(t, err, errBoom)
}

func TestBudgetsProjection(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	food := f.create(t, domain.BudgetInput{CategoryID: "food", Limit: 1000})
	december := november.AddMonths(1)
	f.ledger.spend(december, "food", 600)
	f.ledger.spend(november, "food", 100)

	dec, err := f.svc.List(ctx, "u1", december, domain.Date{})
	require.NoError(t, err)
	assert.True(t, dec.Projected)
	assert.Equal(t, december, dec.Month)
	require.Len(t, dec.Budgets, 1)
	got := dec.Budgets[0]
	assert.Empty(t, got.Budget.ID, "created when the month starts")
	assert.Equal(t, december, got.Budget.Month)
	assert.Equal(t, food.Budget.Limit, got.Budget.Limit)
	assert.Equal(t, int64(600), got.Spent, "what is already spent in December (installments)")
	assert.Equal(t, []string{"2026-11"}, f.store.months("u1"), "a projection stores nothing")
	assert.Equal(t, november, f.marker.through["u1"])
}

func TestBudgetsReadFailures(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	u := f.create(t, domain.BudgetInput{CategoryID: "food", Limit: 1000})

	f.ledger.err = errBoom
	_, err := f.svc.List(ctx, "u1", november, domain.Date{})
	assert.ErrorIs(t, err, errBoom)
	_, err = f.svc.Get(ctx, "u1", u.Budget.ID, domain.Date{})
	assert.ErrorIs(t, err, errBoom)
	_, err = f.svc.Create(ctx, "u1", domain.BudgetInput{CategoryID: "fun", Currency: "BRL", Limit: 1}, domain.Date{})
	assert.ErrorIs(t, err, errBoom)
	_, err = f.svc.Update(ctx, "u1", u.Budget.ID, domain.BudgetPatch{Limit: ptr(int64(5))}, domain.Date{})
	assert.ErrorIs(t, err, errBoom)
	f.ledger.err = nil

	f.recurrences.err = errBoom
	_, err = f.svc.List(ctx, "u1", november, domain.Date{})
	assert.ErrorIs(t, err, errBoom)
}

func ptr[T any](v T) *T { return &v }
