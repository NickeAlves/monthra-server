package domain

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rentInput() RecurrenceInput {
	return RecurrenceInput{
		Kind: "expense", AccountID: "bank", CategoryID: "food", Amount: 250000, Description: " Rent ",
		Tags: []string{"Home"}, Frequency: "monthly", StartDate: "2026-08-05",
	}
}

func rule(t *testing.T, f Frequency, interval int, start, end string) RecurrenceRule {
	t.Helper()
	r := RecurrenceRule{Frequency: f, Interval: interval, Start: day(t, start)}
	if end != "" {
		r.End = day(t, end)
	}
	return r
}

func nth(r RecurrenceRule, n int) []string {
	out := make([]string, n)
	for k := range n {
		out[k] = r.Nth(k).String()
	}
	return out
}

func TestRecurrenceRuleNth(t *testing.T) {
	assert.Equal(t, []string{"2027-01-31", "2027-02-28", "2027-03-31", "2027-04-30"},
		nth(rule(t, FrequencyMonthly, 1, "2027-01-31", ""), 4), "the original day comes back")
	assert.Equal(t, []string{"2024-02-29", "2025-02-28", "2026-02-28", "2027-02-28", "2028-02-29"},
		nth(rule(t, FrequencyYearly, 1, "2024-02-29", ""), 5))
	assert.Equal(t, []string{"2026-11-02", "2026-11-16", "2026-11-30", "2026-12-14"},
		nth(rule(t, FrequencyWeekly, 2, "2026-11-02", ""), 4))
	assert.Equal(t, []string{"2026-12-30", "2027-01-02", "2027-01-05"},
		nth(rule(t, FrequencyDaily, 3, "2026-12-30", ""), 3))
	assert.Equal(t, []string{"2026-01-31", "2026-07-31", "2027-01-31"},
		nth(rule(t, FrequencyMonthly, 6, "2026-01-31", ""), 3))
}

func TestRecurrenceRuleNextOn(t *testing.T) {
	for name, tc := range map[string]struct {
		rule     RecurrenceRule
		on, want string
	}{
		"before the start":           {rule(t, FrequencyMonthly, 1, "2026-08-05", ""), "2020-01-01", "2026-08-05"},
		"on an occurrence":           {rule(t, FrequencyMonthly, 1, "2026-08-05", ""), "2026-11-05", "2026-11-05"},
		"between two":                {rule(t, FrequencyMonthly, 1, "2026-08-05", ""), "2026-11-06", "2026-12-05"},
		"clamped in a short month":   {rule(t, FrequencyMonthly, 1, "2027-01-31", ""), "2027-02-01", "2027-02-28"},
		"after a clamped day":        {rule(t, FrequencyMonthly, 1, "2027-01-31", ""), "2027-03-01", "2027-03-31"},
		"every other month":          {rule(t, FrequencyMonthly, 2, "2026-01-10", ""), "2026-02-01", "2026-03-10"},
		"every other month, its day": {rule(t, FrequencyMonthly, 2, "2026-01-10", ""), "2026-03-11", "2026-05-10"},
		"weekly":                     {rule(t, FrequencyWeekly, 2, "2026-11-02", ""), "2026-11-03", "2026-11-16"},
		"daily":                      {rule(t, FrequencyDaily, 3, "2026-12-30", ""), "2027-01-01", "2027-01-02"},
		"yearly on a leap day":       {rule(t, FrequencyYearly, 1, "2024-02-29", ""), "2027-03-01", "2028-02-29"},
		"the end is included":        {rule(t, FrequencyMonthly, 1, "2026-08-05", "2026-12-05"), "2026-12-01", "2026-12-05"},
		"past the end":               {rule(t, FrequencyMonthly, 1, "2026-08-05", "2026-12-04"), "2026-12-01", ""},
		"past MaxDate":               {rule(t, FrequencyYearly, 1, "2099-12-31", ""), "2100-12-31", "2100-12-31"},
		"after MaxDate":              {rule(t, FrequencyYearly, 1, "2100-06-01", ""), "2100-06-02", ""},
	} {
		got, ok := tc.rule.NextOn(day(t, tc.on))
		if tc.want == "" {
			assert.False(t, ok, name)
			continue
		}
		require.True(t, ok, name)
		assert.Equal(t, tc.want, got.String(), name)
	}
}

func TestNewRecurrence(t *testing.T) {
	r, err := NewRecurrence("u1", rentInput(), expenseRefs, txNow)
	require.NoError(t, err)
	assert.Equal(t, Recurrence{
		UserID: "u1", Kind: TransactionExpense, AccountID: "bank", Currency: "BRL", CategoryID: "food",
		Amount: 250000, Description: "Rent", Tags: []string{"home"},
		Rule: RecurrenceRule{Frequency: FrequencyMonthly, Interval: 1, Start: day(t, "2026-08-05")},
		Next: day(t, "2026-08-05"), Source: SourceManual, CreatedAt: txNow.UTC(), UpdatedAt: txNow.UTC(),
	}, r)
	assert.True(t, r.Active())

	salary := RecurrenceInput{
		Kind: "income", AccountID: "bank", CategoryID: "salary", Amount: 900000, Frequency: "monthly",
		StartDate: "2026-11-05", Count: 12,
	}
	r, err = NewRecurrence("u1", salary, TransactionRefs{Account: txBank, Category: txSalary}, txNow)
	require.NoError(t, err)
	assert.Equal(t, "2027-10-05", r.Rule.End.String(), "count ends on its last occurrence")

	saving := RecurrenceInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "dollars", Amount: 50000, ToAmount: ptr(int64(9000)),
		Frequency: "weekly", Interval: 2, StartDate: "2026-11-02", EndDate: "2027-11-02",
	}
	r, err = NewRecurrence("u1", saving, TransactionRefs{Account: txBank, ToAccount: txDollars}, txNow)
	require.NoError(t, err)
	assert.Equal(t, "USD", r.ToCurrency)
	assert.Equal(t, int64(9000), r.ToAmount)
	assert.Equal(t, 2, r.Rule.Interval)
}

func TestNewRecurrenceValidation(t *testing.T) {
	with := func(change func(*RecurrenceInput)) RecurrenceInput {
		in := rentInput()
		change(&in)
		return in
	}
	for name, tc := range map[string]struct {
		in   RecurrenceInput
		refs TransactionRefs
		want map[string]string
	}{
		"no start":       {with(func(in *RecurrenceInput) { in.StartDate = "" }), expenseRefs, map[string]string{"startDate": CodeRequired}},
		"start too late": {with(func(in *RecurrenceInput) { in.StartDate = "2101-01-01" }), expenseRefs, map[string]string{"startDate": CodeOutOfRange}},
		"no frequency":   {with(func(in *RecurrenceInput) { in.Frequency = "" }), expenseRefs, map[string]string{"frequency": CodeRequired}},
		"bad frequency":  {with(func(in *RecurrenceInput) { in.Frequency = "hourly" }), expenseRefs, map[string]string{"frequency": CodeInvalid}},
		"interval":       {with(func(in *RecurrenceInput) { in.Interval = 100 }), expenseRefs, map[string]string{"interval": CodeOutOfRange}},
		"negative":       {with(func(in *RecurrenceInput) { in.Interval = -1 }), expenseRefs, map[string]string{"interval": CodeOutOfRange}},
		"end before":     {with(func(in *RecurrenceInput) { in.EndDate = "2026-08-04" }), expenseRefs, map[string]string{"endDate": CodeInvalid}},
		"bad end":        {with(func(in *RecurrenceInput) { in.EndDate = "2026-02-30" }), expenseRefs, map[string]string{"endDate": CodeInvalid}},
		"count and end": {with(func(in *RecurrenceInput) { in.Count, in.EndDate = 3, "2027-01-01" }), expenseRefs,
			map[string]string{"count": CodeInvalid}},
		"count":            {with(func(in *RecurrenceInput) { in.Count = MaxRecurrenceCount + 1 }), expenseRefs, map[string]string{"count": CodeOutOfRange}},
		"count past 2100":  {with(func(in *RecurrenceInput) { in.Count, in.Frequency = 100, "yearly" }), expenseRefs, map[string]string{"count": CodeOutOfRange}},
		"archived account": {with(func(in *RecurrenceInput) { in.AccountID = "old" }), TransactionRefs{Account: txOld, Category: txFood}, map[string]string{"accountId": CodeArchived}},
		"income category": {with(func(in *RecurrenceInput) { in.CategoryID = "salary" }), TransactionRefs{Account: txBank, Category: txSalary},
			map[string]string{"categoryId": CodeKindMismatch}},
		"amount": {with(func(in *RecurrenceInput) { in.Amount = 0 }), expenseRefs, map[string]string{"amount": CodeOutOfRange}},
	} {
		_, err := NewRecurrence("u1", tc.in, tc.refs, txNow)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}
}

// stored returns r as the store holds it, with id "r", its occurrences
// created through last (none when empty).
func stored(t *testing.T, r Recurrence, last string) Recurrence {
	t.Helper()
	r.ID = "r"
	if last != "" {
		r.Last = day(t, last)
		r.Next, _ = r.Rule.NextOn(r.Last.AddDays(1))
	}
	return r
}

func rent(t *testing.T, last string) Recurrence {
	t.Helper()
	r, err := NewRecurrence("u1", rentInput(), expenseRefs, txNow)
	require.NoError(t, err)
	return stored(t, r, last)
}

func TestRecurrencePatch(t *testing.T) {
	later := txNow.Add(time.Hour)
	current := rent(t, "2026-11-05")
	require.Equal(t, "2026-12-05", current.Next.String())

	next, changed, err := RecurrencePatch{Amount: ptr(int64(270000)), Tags: ptr([]string{"house"})}.Apply(current, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, int64(270000), next.Amount)
	assert.Equal(t, []string{"house"}, next.Tags)
	assert.Equal(t, current.Next, next.Next, "the rule stays")
	assert.Equal(t, later.UTC(), next.UpdatedAt)

	// The whole form, as it was: nothing changes, nothing is refused.
	form := RecurrencePatch{
		AccountID: ptr("bank"), CategoryID: ptr("food"), Amount: ptr(int64(250000)), Description: ptr("Rent"),
		Tags: ptr([]string{"home"}), Frequency: ptr("monthly"), Interval: ptr(1), StartDate: ptr("2026-08-05"), EndDate: ptr(""),
	}
	same, changed, err := form.Apply(current, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, current, same)

	// The rent moves to the 10th from December on.
	next, changed, err = RecurrencePatch{StartDate: ptr("2026-12-10")}.Apply(current, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "2026-12-10", next.Next.String())

	_, _, err = RecurrencePatch{StartDate: ptr("2026-11-05")}.Apply(current, TransactionRefs{}, later)
	assert.Equal(t, map[string]string{"startDate": CodeOutOfRange}, fieldsOf(t, err), "never over the days given")

	// Weekly from the same start (a Wednesday): the first Wednesday after
	// the last occurrence.
	next, _, err = RecurrencePatch{Frequency: ptr("weekly")}.Apply(current, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.Equal(t, "2026-11-11", next.Next.String())

	ended, _, err := RecurrencePatch{EndDate: ptr("2026-11-30")}.Apply(current, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.False(t, ended.Active())
	back, _, err := RecurrencePatch{EndDate: ptr("")}.Apply(ended, TransactionRefs{Account: txBank}, later)
	require.NoError(t, err)
	assert.Equal(t, "2026-12-05", back.Next.String(), "no end again")
	assert.True(t, back.Rule.End.IsZero())

	onOld := ended
	onOld.AccountID = "old"
	_, _, err = RecurrencePatch{EndDate: ptr("")}.Apply(onOld, TransactionRefs{Account: txOld}, later)
	assert.Equal(t, map[string]string{"accountId": CodeArchived}, fieldsOf(t, err), "never active on an archived account")

	moved, _, err := RecurrencePatch{AccountID: ptr("dollars")}.Apply(current, TransactionRefs{Account: txDollars}, later)
	require.NoError(t, err)
	assert.Equal(t, "USD", moved.Currency)

	for name, tc := range map[string]struct {
		patch RecurrencePatch
		want  map[string]string
	}{
		"frequency": {RecurrencePatch{Frequency: ptr("hourly")}, map[string]string{"frequency": CodeInvalid}},
		"interval":  {RecurrencePatch{Interval: ptr(0)}, map[string]string{"interval": CodeOutOfRange}},
		"start":     {RecurrencePatch{StartDate: ptr("2026-13-01")}, map[string]string{"startDate": CodeInvalid}},
		"end":       {RecurrencePatch{EndDate: ptr("2026-08-01")}, map[string]string{"endDate": CodeInvalid}},
		"amount":    {RecurrencePatch{Amount: ptr(int64(0))}, map[string]string{"amount": CodeOutOfRange}},
		"account":   {RecurrencePatch{AccountID: ptr("old")}, map[string]string{"accountId": CodeArchived}},
	} {
		_, _, err := tc.patch.Apply(current, TransactionRefs{Account: txOld}, later)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}
}

func TestRecurrenceOccurrences(t *testing.T) {
	r := rent(t, "")
	ts, next := r.Occurrences(expenseRefs, day(t, "2026-11-08"), 100, txNow)
	assert.Equal(t, []string{"2026-08-05", "2026-09-05", "2026-10-05", "2026-11-05"}, dates(ts))
	assert.Equal(t, "2026-12-05", next.String())
	for _, o := range ts {
		assert.Equal(t, &RecurrenceLink{ID: "r", Date: o.Date}, o.Recurrence)
		assert.Equal(t, int64(250000), o.Amount)
		assert.Equal(t, "Rent", o.Description)
		assert.True(t, o.InvoiceMonth.IsZero(), "off a card")
		assert.Equal(t, txNow.UTC(), o.CreatedAt)
	}
	ts[0].Tags[0] = "changed"
	assert.Equal(t, []string{"home"}, r.Tags, "each occurrence has its own tags")

	ts, next = r.Occurrences(expenseRefs, day(t, "2026-11-08"), 2, txNow)
	assert.Len(t, ts, 2)
	assert.Equal(t, "2026-10-05", next.String(), "the rest comes later")
	ts, next = r.Occurrences(expenseRefs, day(t, "2026-08-04"), 100, txNow)
	assert.Empty(t, ts)
	assert.Equal(t, "2026-08-05", next.String())

	r.Rule.End = day(t, "2026-09-30")
	ts, next = r.Occurrences(expenseRefs, day(t, "2026-11-08"), 100, txNow)
	assert.Len(t, ts, 2)
	assert.True(t, next.IsZero(), "ended")

	streaming := rent(t, "")
	streaming.AccountID, streaming.Rule.Start, streaming.Next = "card", day(t, "2026-10-24"), day(t, "2026-10-24")
	streaming.Rule.Frequency = FrequencyDaily
	ts, _ = streaming.Occurrences(TransactionRefs{Account: txCard}, day(t, "2026-10-25"), 100, txNow)
	assert.Equal(t, []string{"2026-11", "2026-12"}, invoices(ts), "the closing day goes to the next invoice")

	payment, err := NewRecurrence("u1", RecurrenceInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "card", Amount: 10000, Frequency: "monthly", StartDate: "2026-11-06",
	}, TransactionRefs{Account: txBank, ToAccount: txCard}, txNow)
	require.NoError(t, err)
	ts, _ = payment.Occurrences(TransactionRefs{Account: txBank, ToAccount: txCard}, day(t, "2026-12-06"), 100, txNow)
	require.Len(t, ts, 2)
	assert.Equal(t, []string{"2026-11", "2026-12"}, []string{ts[0].ToInvoiceMonth.String(), ts[1].ToInvoiceMonth.String()},
		"a payment goes to the last invoice closed")
	assert.True(t, ts[0].InvoiceMonth.IsZero())
}

func TestRecurrenceFollow(t *testing.T) {
	later := txNow.Add(time.Hour)
	r := rent(t, "2026-11-05")
	ts, _ := rent(t, "").Occurrences(expenseRefs, day(t, "2026-11-05"), 100, txNow)
	before := ts[3]
	after := before
	after.Amount, after.AccountID, after.Currency = 270000, "dollars", "USD"

	next, changed := r.Follow(before, after, later)
	assert.True(t, changed)
	assert.Equal(t, int64(270000), next.Amount)
	assert.Equal(t, "dollars", next.AccountID)
	assert.Equal(t, "USD", next.Currency)
	assert.Equal(t, "Rent", next.Description, "what did not change stays")
	assert.Equal(t, later.UTC(), next.UpdatedAt)

	_, changed = r.Follow(before, before, later)
	assert.False(t, changed)

	saving, err := NewRecurrence("u1", RecurrenceInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "wallet", Amount: 50000, Frequency: "monthly", StartDate: "2026-11-02",
	}, TransactionRefs{Account: txBank, ToAccount: txWallet}, txNow)
	require.NoError(t, err)
	tx := saving.template(day(t, "2026-11-02"))
	moved := tx
	moved.ToAccountID, moved.ToCurrency, moved.ToAmount = "dollars", "USD", 9000
	next, changed = saving.Follow(tx, moved, later)
	assert.True(t, changed)
	assert.Equal(t, "dollars", next.ToAccountID)
	assert.Equal(t, int64(9000), next.ToAmount)
}

func TestRecurrenceEndBefore(t *testing.T) {
	r := rent(t, "2026-11-05")
	ended, keep := r.EndBefore(day(t, "2026-10-05"), false, txNow)
	require.True(t, keep)
	assert.Equal(t, "2026-10-04", ended.Rule.End.String())
	assert.False(t, ended.Active())

	future := rent(t, "")
	future.Rule.End = day(t, "2027-01-05")
	ended, keep = future.EndBefore(day(t, "2027-03-05"), false, txNow)
	require.True(t, keep)
	assert.Equal(t, "2027-01-05", ended.Rule.End.String(), "an earlier end stays")
	assert.True(t, ended.Active())

	_, keep = r.EndBefore(day(t, "2026-08-05"), false, txNow)
	assert.False(t, keep, "nothing would be left")

	// A start moved past older occurrences (August to November): cutting
	// on or before it keeps the recurrence, ended, so those occurrences
	// still name it.
	moved := rent(t, "2026-11-05")
	moved.Rule.Start, moved.Next = day(t, "2027-02-05"), day(t, "2027-02-05")
	ended, keep = moved.EndBefore(day(t, "2026-10-05"), true, txNow)
	require.True(t, keep)
	assert.Equal(t, "2026-10-04", ended.Rule.End.String(), "before the start")
	assert.False(t, ended.Active())
	assert.Equal(t, "2027-02-05", ended.Rule.Start.String())
	assert.Equal(t, txNow.UTC(), ended.UpdatedAt)
	_, keep = moved.EndBefore(day(t, "2026-10-05"), false, txNow)
	assert.False(t, keep, "nothing before the cut")
	ended, keep = moved.EndBefore(day(t, "2027-02-05"), true, txNow)
	require.True(t, keep)
	assert.Equal(t, "2027-02-04", ended.Rule.End.String())
	assert.False(t, ended.Active())
}

// occurrences returns the occurrences of r through day through as the
// store holds them, with ids ("o1", "o2", ...).
func occurrences(t *testing.T, r Recurrence, refs TransactionRefs, through string) []Transaction {
	t.Helper()
	ts, _ := r.Occurrences(refs, day(t, through), 100, txNow)
	for i := range ts {
		ts[i].ID = fmt.Sprintf("o%d", i+1)
	}
	return ts
}

func TestApplyInScopeOnOccurrences(t *testing.T) {
	later := txNow.Add(time.Hour)
	r := rent(t, "")
	r.AccountID, r.Rule.Start, r.Next = "card", day(t, "2026-09-24"), day(t, "2026-09-24")
	ts := occurrences(t, r, TransactionRefs{Account: txCard}, "2026-12-24")
	require.Equal(t, []string{"2026-10", "2026-11", "2026-12", "2027-01"}, invoices(ts))

	edit, err := TransactionPatch{Amount: ptr(int64(270000))}.ApplyInScope(ts[1], ScopeFuture, ts[2:], TransactionRefs{}, later)
	require.NoError(t, err)
	assert.Equal(t, int64(270000), edit.Next.Amount)
	assert.Equal(t, []int64{270000, 270000}, amounts(edit.Others))
	assert.Equal(t, []string{"o3", "o4"}, []string{edit.Others[0].ID, edit.Others[1].ID})

	// Moving to another card from now on: each one picks its invoice by its
	// own day there.
	edit, err = TransactionPatch{AccountID: ptr("card2")}.ApplyInScope(ts[1], ScopeFuture, ts[2:], TransactionRefs{Account: txCard2}, later)
	require.NoError(t, err)
	assert.Equal(t, "2026-11", edit.Next.InvoiceMonth.String())
	assert.Equal(t, []string{"2026-12", "2027-01"}, invoices(edit.Others))
	edit, err = TransactionPatch{AccountID: ptr("bank")}.ApplyInScope(ts[1], ScopeAll, []Transaction{ts[0], ts[2], ts[3]}, TransactionRefs{Account: txBank}, later)
	require.NoError(t, err)
	assert.Equal(t, []string{"", "", ""}, invoices(edit.Others), "off the card")

	// The day and the invoice belong to each occurrence.
	for name, p := range map[string]TransactionPatch{
		"date":    {Date: ptr("2026-10-25")},
		"invoice": {InvoiceMonth: ptr("2026-12")},
	} {
		_, err = p.ApplyInScope(ts[1], ScopeFuture, ts[2:], TransactionRefs{Account: txCard}, later)
		assert.Equal(t, map[string]string{"scope": CodeInvalid}, fieldsOf(t, err), name)
		edit, err = p.ApplyInScope(ts[1], ScopeThis, ts[2:], TransactionRefs{Account: txCard}, later)
		require.NoError(t, err, name)
		assert.Empty(t, edit.Others, name)
	}
	edit, err = TransactionPatch{Date: ptr("2026-10-24"), Description: ptr("Streaming")}.ApplyInScope(ts[1], ScopeAll, ts, TransactionRefs{Account: txCard}, later)
	require.NoError(t, err, "the same day is no change")
	assert.Len(t, edit.Others, 3)

	saving, err := NewRecurrence("u1", RecurrenceInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "wallet", Amount: 50000, Frequency: "monthly", StartDate: "2026-09-02",
	}, TransactionRefs{Account: txBank, ToAccount: txWallet}, txNow)
	require.NoError(t, err)
	saving.ID = "s"
	moves := occurrences(t, saving, TransactionRefs{Account: txBank, ToAccount: txWallet}, "2026-11-02")
	edit, err = TransactionPatch{ToAccountID: ptr("card"), Amount: ptr(int64(60000))}.ApplyInScope(moves[0], ScopeAll, moves[1:],
		TransactionRefs{ToAccount: txCard}, later)
	require.NoError(t, err)
	for _, o := range edit.Others {
		assert.Equal(t, "card", o.ToAccountID)
		assert.Equal(t, int64(60000), o.ToAmount, "what arrives follows the amount")
	}
	assert.Equal(t, []string{"2026-10", "2026-11"}, []string{edit.Others[0].ToInvoiceMonth.String(), edit.Others[1].ToInvoiceMonth.String()})

	edit, err = TransactionPatch{Amount: ptr(int64(1))}.ApplyInScope(ts[1], ScopeAll, moves, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.Empty(t, edit.Others, "another recurrence's occurrences are left alone")
}
