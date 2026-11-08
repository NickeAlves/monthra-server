package transactions

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// occur stores the occurrences of recurrence "r" of the bank account on
// days, as the recurrences create them, and returns them.
func (f fixture) occur(t *testing.T, days ...string) []domain.Transaction {
	t.Helper()
	out := make([]domain.Transaction, len(days))
	for i, d := range days {
		day, err := domain.ParseDate(d)
		require.NoError(t, err)
		tx := domain.Transaction{
			UserID: "u1", Kind: domain.TransactionExpense, AccountID: "bank", Currency: "BRL", CategoryID: "food",
			Amount: 250000, Date: day, Description: "Rent", Tags: []string{}, Source: domain.SourceManual,
			Recurrence: &domain.RecurrenceLink{ID: "r", Date: day}, CreatedAt: t0, UpdatedAt: t0,
		}
		out[i], err = f.store.Insert(context.Background(), tx)
		require.NoError(t, err)
	}
	return out
}

func amountsOf(list []domain.Transaction) []int64 {
	out := make([]int64, len(list))
	for i, t := range list {
		out[i] = t.Amount
	}
	return out
}

func TestListCreatesTheOccurrencesFirst(t *testing.T) {
	f := newFixture()
	asOf, err := domain.ParseDate("2026-11-09")
	require.NoError(t, err)
	_, err = f.svc.List(context.Background(), "u1", Query{AsOf: asOf})
	require.NoError(t, err)
	assert.Equal(t, []string{"materialize:u1@2026-11-09"}, f.series.calls)
}

func TestUpdateOccurrencesInScope(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	list := f.occur(t, "2026-09-05", "2026-10-05", "2026-11-05")

	got, err := f.svc.Update(ctx, "u1", list[1].ID, domain.TransactionPatch{Amount: ptr(int64(270000))}, domain.ScopeFuture)
	require.NoError(t, err)
	assert.Equal(t, int64(270000), got.Amount)
	now, err := f.store.Occurrences(ctx, "u1", "r", domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []int64{250000, 270000, 270000}, amountsOf(now), "the earlier one stays")
	assert.Equal(t, []string{"follow:r:250000->270000"}, f.series.calls, "the recurrence follows")

	f.series.calls = nil
	_, err = f.svc.Update(ctx, "u1", list[0].ID, domain.TransactionPatch{Description: ptr("Flat")}, domain.ScopeThis)
	require.NoError(t, err)
	assert.Empty(t, f.series.calls, "this one only: the recurrence stays")

	_, err = f.svc.Update(ctx, "u1", list[0].ID, domain.TransactionPatch{Date: ptr("2026-09-06")}, domain.ScopeAll)
	assert.Equal(t, map[string]string{"scope": domain.CodeInvalid}, fields(t, err), "the rule gives the days")
	_, err = f.svc.Update(ctx, "u1", list[0].ID, domain.TransactionPatch{Date: ptr("2026-09-06")}, domain.ScopeThis)
	require.NoError(t, err, "one occurrence moves alone")

	// Moving the account from now on is allowed on a recurrence.
	f.series.calls = nil
	_, err = f.svc.Update(ctx, "u1", list[1].ID, domain.TransactionPatch{AccountID: ptr("card")}, domain.ScopeFuture)
	require.NoError(t, err)
	now, err = f.store.Occurrences(ctx, "u1", "r", domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []string{"bank", "card", "card"}, []string{now[0].AccountID, now[1].AccountID, now[2].AccountID})
	assert.Equal(t, []string{"2026-11", "2026-12"}, []string{now[1].InvoiceMonth.String(), now[2].InvoiceMonth.String()})
	assert.Equal(t, []string{"follow:r:270000->270000"}, f.series.calls)

	// The recurrence follows before the addressed one is stored: a failure
	// leaves it as it was, and the same patch again completes the edit.
	f.series.err = errors.New("boom")
	_, err = f.svc.Update(ctx, "u1", list[1].ID, domain.TransactionPatch{Amount: ptr(int64(1000))}, domain.ScopeAll)
	assert.ErrorContains(t, err, "follow the recurrence")
	addressed, err := f.store.ByID(ctx, "u1", list[1].ID)
	require.NoError(t, err)
	assert.Equal(t, int64(270000), addressed.Amount)
	f.series.err = nil
	_, err = f.svc.Update(ctx, "u1", list[1].ID, domain.TransactionPatch{Amount: ptr(int64(1000))}, domain.ScopeAll)
	require.NoError(t, err)
	now, err = f.store.Occurrences(ctx, "u1", "r", domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []int64{1000, 1000, 1000}, amountsOf(now))

	f.store.err = errors.New("down")
	_, err = f.svc.Update(ctx, "u1", list[1].ID, domain.TransactionPatch{Amount: ptr(int64(2))}, domain.ScopeAll)
	assert.Error(t, err)
}

func TestDeleteOccurrencesInScope(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	list := f.occur(t, "2026-09-05", "2026-10-05", "2026-11-05")
	other := f.occur(t, "2026-11-01")
	other[0].Recurrence.ID = "other"
	require.NoError(t, f.store.Update(ctx, other[0]))

	assert.ErrorIs(t, f.svc.Delete(ctx, "u2", list[1].ID, domain.ScopeFuture), domain.ErrNotFound)
	require.NoError(t, f.svc.Delete(ctx, "u1", list[1].ID, domain.ScopeFuture))
	left, err := f.store.Occurrences(ctx, "u1", "r", domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []string{list[0].ID}, []string{left[0].ID})
	assert.Len(t, left, 1)
	assert.Equal(t, []string{"end:r@2026-10-05"}, f.series.calls, "the rule ends before it")

	f.series.calls = nil
	require.NoError(t, f.svc.Delete(ctx, "u1", list[0].ID, domain.ScopeAll))
	left, err = f.store.Occurrences(ctx, "u1", "r", domain.Date{})
	require.NoError(t, err)
	assert.Empty(t, left)
	assert.Equal(t, []string{"remove:r"}, f.series.calls)
	_, err = f.store.ByID(ctx, "u1", other[0].ID)
	require.NoError(t, err, "another recurrence's occurrence stays")

	f.series.err = errors.New("boom")
	assert.ErrorContains(t, f.svc.Delete(ctx, "u1", other[0].ID, domain.ScopeAll), "end the recurrence")
	_, err = f.store.ByID(ctx, "u1", other[0].ID)
	require.NoError(t, err, "nothing deleted before the recurrence ends")
}

func TestNewServiceDefaults(t *testing.T) {
	ctx := context.Background()
	svc := NewService(newMemStore(), &refs{}, categoryFinder{&refs{}}, Options{})
	assert.NotNil(t, svc.now())
	svc.metrics.TransactionCreated("expense")
	svc.series.Materialize(ctx, "u1", domain.Date{})
	require.NoError(t, svc.series.Follow(ctx, domain.Transaction{}, domain.Transaction{}))
	require.NoError(t, svc.series.EndBefore(ctx, domain.Transaction{}))
	require.NoError(t, svc.series.Remove(ctx, domain.Transaction{}))
}
