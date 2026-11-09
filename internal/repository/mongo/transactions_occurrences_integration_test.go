//go:build integration

package mongo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/NickeAlves/monthra-server/internal/domain"
	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

// occurrencesOf returns the occurrences of a monthly rent of l on acc from
// start through through, as the recurrences create them, under a new
// recurrence id.
func (l ledger) occurrencesOf(t *testing.T, acc *domain.Account, start, through string) []domain.Transaction {
	t.Helper()
	r := l.monthly(t, acc, start)
	r.ID = bson.NewObjectID().Hex()
	ts, _ := r.Occurrences(domain.TransactionRefs{Account: acc}, mustDay(t, through), 100, now)
	return ts
}

func occurrenceDays(list []domain.Transaction) []string {
	out := make([]string, len(list))
	for i, tx := range list {
		out[i] = tx.Recurrence.Date.String()
	}
	return out
}

func TestTransactionsOccurrences(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	store := repo.NewTransactions(db)
	a, b := newLedger(bson.NewObjectID().Hex()), newLedger(bson.NewObjectID().Hex())
	card := newCard(a.owner)
	rent := a.occurrencesOf(t, a.bank, "2026-08-05", "2026-11-05")
	streaming := a.occurrencesOf(t, card, "2026-09-24", "2026-11-24")
	theirs := b.occurrencesOf(t, b.bank, "2026-08-05", "2026-11-05")
	insertAll(t, store, a.spend(t, a.bank, a.food, 100, "2026-11-05", "not an occurrence"))

	n, err := store.InsertOccurrences(ctx, rent)
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	_, err = store.InsertOccurrences(ctx, streaming)
	require.NoError(t, err)
	_, err = store.InsertOccurrences(ctx, theirs)
	require.NoError(t, err)

	// The same occurrences again (a second reader, or a pass that never
	// advanced its recurrence) are skipped, the new ones stored.
	n, err = store.InsertOccurrences(ctx, rent[2:])
	require.NoError(t, err)
	assert.Zero(t, n)
	recID := rent[0].Recurrence.ID
	more := a.occurrencesOf(t, a.bank, "2026-08-05", "2026-12-05")
	for i := range more {
		more[i].Recurrence.ID = recID
	}
	n, err = store.InsertOccurrences(ctx, more)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "December only")
	n, err = store.InsertOccurrences(ctx, nil)
	require.NoError(t, err)
	assert.Zero(t, n)
	_, err = store.InsertOccurrences(ctx, []domain.Transaction{a.spend(t, a.bank, a.food, 1, "2026-11-05", "")})
	assert.Error(t, err, "not an occurrence")

	all, err := store.Occurrences(ctx, a.owner, recID, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-08-05", "2026-09-05", "2026-10-05", "2026-11-05", "2026-12-05"}, occurrenceDays(all))
	assert.Equal(t, rent[0].Recurrence, all[0].Recurrence)
	later, err := store.Occurrences(ctx, a.owner, recID, mustDay(t, "2026-10-05"))
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-10-05", "2026-11-05", "2026-12-05"}, occurrenceDays(later))
	cards, err := store.Occurrences(ctx, a.owner, streaming[0].Recurrence.ID, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-10", "2026-11", "2026-12"}, []string{
		cards[0].InvoiceMonth.String(), cards[1].InvoiceMonth.String(), cards[2].InvoiceMonth.String(),
	})

	listed, err := store.List(ctx, a.owner, domain.TransactionFilter{RecurrenceID: recID}, nil, 50)
	require.NoError(t, err)
	assert.Len(t, listed, 5)
	assert.Equal(t, "2026-12-05", listed[0].Date.String(), "newest first")
	listed, err = store.List(ctx, a.owner, domain.TransactionFilter{RecurrenceID: "not-an-id"}, nil, 50)
	require.NoError(t, err)
	assert.Empty(t, listed)

	// Earlier occurrences: strictly before the day, of this recurrence and owner.
	earlier, err := store.HasOccurrencesBefore(ctx, a.owner, recID, mustDay(t, "2026-08-06"))
	require.NoError(t, err)
	assert.True(t, earlier)
	earlier, err = store.HasOccurrencesBefore(ctx, a.owner, recID, mustDay(t, "2026-08-05"))
	require.NoError(t, err)
	assert.False(t, earlier, "the first one is not before itself")
	earlier, err = store.HasOccurrencesBefore(ctx, b.owner, recID, mustDay(t, "2027-01-01"))
	require.NoError(t, err)
	assert.False(t, earlier, "another user's recurrence")
	earlier, err = store.HasOccurrencesBefore(ctx, a.owner, "not-an-id", mustDay(t, "2027-01-01"))
	require.NoError(t, err)
	assert.False(t, earlier)

	// Another user never reads or deletes them.
	got, err := store.Occurrences(ctx, b.owner, recID, domain.Date{})
	require.NoError(t, err)
	assert.Empty(t, got)
	listed, err = store.List(ctx, b.owner, domain.TransactionFilter{RecurrenceID: recID}, nil, 50)
	require.NoError(t, err)
	assert.Empty(t, listed)
	require.NoError(t, store.DeleteOccurrences(ctx, b.owner, recID, domain.Date{}))
	got, err = store.Occurrences(ctx, a.owner, recID, domain.Date{})
	require.NoError(t, err)
	assert.Len(t, got, 5)
	got, err = store.Occurrences(ctx, a.owner, "not-an-id", domain.Date{})
	require.NoError(t, err)
	assert.Empty(t, got)
	require.NoError(t, store.DeleteOccurrences(ctx, a.owner, "not-an-id", domain.Date{}))

	// A scoped edit keeps the link and the rule day.
	edit, err := domain.TransactionPatch{Amount: ptr(int64(270000))}.ApplyInScope(all[2], domain.ScopeFuture, later, domain.TransactionRefs{}, now)
	require.NoError(t, err)
	require.NoError(t, store.UpdateMany(ctx, append(edit.Others, edit.Next)))
	later, err = store.Occurrences(ctx, a.owner, recID, mustDay(t, "2026-10-05"))
	require.NoError(t, err)
	for _, o := range later {
		assert.Equal(t, int64(270000), o.Amount)
		assert.Equal(t, recID, o.Recurrence.ID)
	}

	require.NoError(t, store.DeleteOccurrences(ctx, a.owner, recID, mustDay(t, "2026-11-05")))
	got, err = store.Occurrences(ctx, a.owner, recID, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-08-05", "2026-09-05", "2026-10-05"}, occurrenceDays(got))
	require.NoError(t, store.DeleteOccurrences(ctx, a.owner, recID, domain.Date{}))
	got, err = store.Occurrences(ctx, a.owner, recID, domain.Date{})
	require.NoError(t, err)
	assert.Empty(t, got)
	left, err := store.Occurrences(ctx, b.owner, theirs[0].Recurrence.ID, domain.Date{})
	require.NoError(t, err)
	assert.Len(t, left, 4, "the other user's stay")
}

func TestOccurrencesQueriesReadOnlyTheirEntries(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	store := repo.NewTransactions(db)
	l := newLedger(bson.NewObjectID().Hex())
	// A rent of three years beside a daily habit and plain spending: the
	// edits and listings of one recurrence read its occurrences only.
	rent := l.occurrencesOf(t, l.bank, "2024-01-05", "2026-12-05")
	_, err := store.InsertOccurrences(ctx, rent)
	require.NoError(t, err)
	habit := l.monthly(t, l.bank, "2026-01-01")
	habit.ID, habit.Rule.Frequency = bson.NewObjectID().Hex(), domain.FrequencyDaily
	daily, _ := habit.Occurrences(domain.TransactionRefs{Account: l.bank}, mustDay(t, "2026-11-08"), 400, now)
	_, err = store.InsertOccurrences(ctx, daily)
	require.NoError(t, err)
	for range 30 {
		insertAll(t, store, l.spend(t, l.bank, l.food, 100, "2026-11-01", ""))
	}
	recID := rent[0].Recurrence.ID

	op := profile(t, db, func() {
		list, err := store.Occurrences(ctx, l.owner, recID, mustDay(t, "2026-10-05"))
		require.NoError(t, err)
		assert.Len(t, list, 3)
	})
	assert.LessOrEqual(t, op.KeysExamined, 4, "the future ones of one recurrence: %s", op.PlanSummary)

	op = profile(t, db, func() {
		list, err := store.List(ctx, l.owner, domain.TransactionFilter{RecurrenceID: recID}, nil, 11)
		require.NoError(t, err)
		assert.Len(t, list, 11)
	})
	assert.LessOrEqual(t, op.KeysExamined, 12, "a page of one recurrence: %s", op.PlanSummary)
	assert.NotContains(t, op.PlanSummary, "SORT {", "the index gives the order")

	op = profile(t, db, func() {
		earlier, err := store.HasOccurrencesBefore(ctx, l.owner, recID, mustDay(t, "2026-12-05"))
		require.NoError(t, err)
		assert.True(t, earlier)
	})
	assert.LessOrEqual(t, op.KeysExamined, 2, "the first earlier one: %s", op.PlanSummary)

	op = profile(t, db, func() {
		require.NoError(t, store.DeleteOccurrences(ctx, l.owner, recID, mustDay(t, "2026-12-05")))
	})
	assert.LessOrEqual(t, op.KeysExamined, 2, "deleting the future ones: %s", op.PlanSummary)
}
