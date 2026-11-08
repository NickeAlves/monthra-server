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

// monthly returns a monthly expense recurrence of l on acc from start.
func (l ledger) monthly(t *testing.T, acc *domain.Account, start string) domain.Recurrence {
	t.Helper()
	r, err := domain.NewRecurrence(l.owner, domain.RecurrenceInput{
		Kind: "expense", AccountID: acc.ID, CategoryID: l.food.ID, Amount: 250000, Description: "Rent",
		Tags: []string{"home"}, Frequency: "monthly", StartDate: start,
	}, domain.TransactionRefs{Account: acc, Category: l.food}, now)
	require.NoError(t, err)
	return r
}

// saving returns a weekly transfer recurrence of l from start.
func (l ledger) saving(t *testing.T, from, to *domain.Account, start string) domain.Recurrence {
	t.Helper()
	r, err := domain.NewRecurrence(l.owner, domain.RecurrenceInput{
		Kind: "transfer", AccountID: from.ID, ToAccountID: to.ID, Amount: 50000, ToAmount: ptr(int64(9000)),
		Frequency: "weekly", StartDate: start, EndDate: "2027-11-02",
	}, domain.TransactionRefs{Account: from, ToAccount: to}, now)
	require.NoError(t, err)
	return r
}

func TestRecurrences(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	store := repo.NewRecurrences(db)
	a, b := newLedger(bson.NewObjectID().Hex()), newLedger(bson.NewObjectID().Hex())

	rent, err := store.Insert(ctx, a.monthly(t, a.bank, "2026-08-05"))
	require.NoError(t, err)
	require.Len(t, rent.ID, 24)
	saving, err := store.Insert(ctx, a.saving(t, a.bank, a.dollars, "2026-11-02"))
	require.NoError(t, err)
	theirs, err := store.Insert(ctx, b.monthly(t, b.bank, "2026-08-05"))
	require.NoError(t, err)

	got, err := store.ByID(ctx, a.owner, rent.ID)
	require.NoError(t, err)
	assert.Equal(t, rent, got)
	got, err = store.ByID(ctx, a.owner, saving.ID)
	require.NoError(t, err)
	assert.Equal(t, saving, got, "a transfer, with an end")
	assert.Equal(t, "USD", got.ToCurrency)

	list, err := store.List(ctx, a.owner)
	require.NoError(t, err)
	assert.Equal(t, []string{rent.ID, saving.ID}, []string{list[0].ID, list[1].ID})
	n, err := store.Count(ctx, a.owner)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// Another user's recurrence answers like one that does not exist.
	_, err = store.ByID(ctx, b.owner, rent.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	hijack := rent
	hijack.UserID = b.owner
	assert.ErrorIs(t, store.Update(ctx, hijack), domain.ErrNotFound)
	assert.ErrorIs(t, store.Delete(ctx, b.owner, rent.ID), domain.ErrNotFound)
	require.NoError(t, store.Advance(ctx, b.owner, rent.ID, rent.Next, rent.Next.AddDays(31), rent.Next))
	_, err = store.ByID(ctx, "not-an-id", rent.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	empty, err := store.List(ctx, "not-an-id")
	require.NoError(t, err)
	assert.Empty(t, empty)

	// Due: the active ones whose next day has come, earliest first.
	due, err := store.Due(ctx, a.owner, mustDay(t, "2026-11-08"), 10)
	require.NoError(t, err)
	assert.Equal(t, []string{rent.ID, saving.ID}, recIDs(due))
	due, err = store.Due(ctx, a.owner, mustDay(t, "2026-11-01"), 10)
	require.NoError(t, err)
	assert.Equal(t, []string{rent.ID}, recIDs(due))
	due, err = store.Due(ctx, a.owner, mustDay(t, "2026-11-08"), 1)
	require.NoError(t, err)
	assert.Len(t, due, 1)

	// Advance is a compare-and-set on the next day.
	require.NoError(t, store.Advance(ctx, a.owner, rent.ID, rent.Next, mustDay(t, "2026-12-05"), mustDay(t, "2026-11-05")))
	require.NoError(t, store.Advance(ctx, a.owner, rent.ID, rent.Next, mustDay(t, "2027-01-05"), mustDay(t, "2026-12-05")))
	got, err = store.ByID(ctx, a.owner, rent.ID)
	require.NoError(t, err)
	assert.Equal(t, "2026-12-05", got.Next.String(), "the second one lost")
	assert.Equal(t, "2026-11-05", got.Last.String())
	stillTheirs, err := store.ByID(ctx, b.owner, theirs.ID)
	require.NoError(t, err)
	assert.Equal(t, theirs, stillTheirs)

	// Ending it removes next_date (never null), so it leaves the due index.
	require.NoError(t, store.Advance(ctx, a.owner, saving.ID, saving.Next, domain.Date{}, mustDay(t, "2027-11-01")))
	var raw bson.M
	oid, err := bson.ObjectIDFromHex(saving.ID)
	require.NoError(t, err)
	require.NoError(t, db.Collection(repo.CollRecurrences).FindOne(ctx, bson.D{{Key: "_id", Value: oid}}).Decode(&raw))
	assert.NotContains(t, raw, "next_date")
	assert.NotContains(t, raw, "category_id", "a transfer has no category")

	// Update stores the template and the rule; Last stays.
	got.Amount, got.Description, got.Tags = 270000, "Flat", []string{"house"}
	got.Rule.End, got.Next = mustDay(t, "2026-11-30"), domain.Date{}
	got.UpdatedAt = now.Add(1)
	require.NoError(t, store.Update(ctx, got))
	stored, err := store.ByID(ctx, a.owner, rent.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(270000), stored.Amount)
	assert.Equal(t, "2026-11-30", stored.Rule.End.String())
	assert.False(t, stored.Active())
	assert.Equal(t, "2026-11-05", stored.Last.String())
	stored.Rule.End, stored.Next = domain.Date{}, mustDay(t, "2026-12-05")
	require.NoError(t, store.Update(ctx, stored))
	again, err := store.ByID(ctx, a.owner, rent.ID)
	require.NoError(t, err)
	assert.True(t, again.Rule.End.IsZero(), "no end again")
	assert.True(t, again.Active())

	require.NoError(t, store.Delete(ctx, a.owner, rent.ID))
	assert.ErrorIs(t, store.Delete(ctx, a.owner, rent.ID), domain.ErrNotFound)
}

func TestRecurrencesUsage(t *testing.T) {
	ctx := context.Background()
	store := repo.NewRecurrences(testDB(t))
	a, b := newLedger(bson.NewObjectID().Hex()), newLedger(bson.NewObjectID().Hex())
	rent, err := store.Insert(ctx, a.monthly(t, a.bank, "2026-08-05"))
	require.NoError(t, err)
	saving, err := store.Insert(ctx, a.saving(t, a.wallet, a.dollars, "2026-11-02"))
	require.NoError(t, err)
	require.NoError(t, store.Advance(ctx, a.owner, saving.ID, saving.Next, domain.Date{}, saving.Next))

	for name, tc := range map[string]struct {
		owner, account string
		used, active   bool
	}{
		"its account":         {a.owner, a.bank.ID, true, true},
		"an ended one's":      {a.owner, a.wallet.ID, true, false},
		"a destination":       {a.owner, a.dollars.ID, true, false},
		"none":                {a.owner, bson.NewObjectID().Hex(), false, false},
		"another user's":      {b.owner, a.bank.ID, false, false},
		"an id that is not":   {a.owner, "not-an-id", false, false},
		"an owner who is not": {"not-an-id", a.bank.ID, false, false},
	} {
		used, active, err := store.AccountUsage(ctx, tc.owner, tc.account)
		require.NoError(t, err, name)
		assert.Equal(t, [2]bool{tc.used, tc.active}, [2]bool{used, active}, name)
	}
	// An active one wins over an ended one on the same account.
	_, err = store.Insert(ctx, a.monthly(t, a.wallet, "2027-01-01"))
	require.NoError(t, err)
	_, active, err := store.AccountUsage(ctx, a.owner, a.wallet.ID)
	require.NoError(t, err)
	assert.True(t, active)

	inUse, err := store.CategoryInUse(ctx, a.owner, a.food.ID)
	require.NoError(t, err)
	assert.True(t, inUse)
	inUse, err = store.CategoryInUse(ctx, b.owner, a.food.ID)
	require.NoError(t, err)
	assert.False(t, inUse)
	inUse, err = store.CategoryInUse(ctx, a.owner, "not-an-id")
	require.NoError(t, err)
	assert.False(t, inUse)

	require.NoError(t, store.ReassignCategory(ctx, b.owner, a.food.ID, a.otherCat.ID, now))
	got, err := store.ByID(ctx, a.owner, rent.ID)
	require.NoError(t, err)
	assert.Equal(t, a.food.ID, got.CategoryID, "another user moves nothing")
	require.NoError(t, store.ReassignCategory(ctx, a.owner, a.food.ID, a.otherCat.ID, now))
	got, err = store.ByID(ctx, a.owner, rent.ID)
	require.NoError(t, err)
	assert.Equal(t, a.otherCat.ID, got.CategoryID)
	assert.Error(t, store.ReassignCategory(ctx, a.owner, "not-an-id", a.otherCat.ID, now))
}

func TestRecurrencesDueReadsOnlyTheDueOnes(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	store := repo.NewRecurrences(db)
	l := newLedger(bson.NewObjectID().Hex())
	// 30 ended recurrences and 10 active ones not due yet: the read before
	// every listing must find the due one without reading them.
	for range 30 {
		r, err := store.Insert(ctx, l.monthly(t, l.bank, "2026-01-05"))
		require.NoError(t, err)
		require.NoError(t, store.Advance(ctx, l.owner, r.ID, r.Next, domain.Date{}, r.Next))
	}
	for range 10 {
		_, err := store.Insert(ctx, l.monthly(t, l.bank, "2027-01-05"))
		require.NoError(t, err)
	}
	due, err := store.Insert(ctx, l.monthly(t, l.bank, "2026-11-05"))
	require.NoError(t, err)

	op := profileOn(t, db, repo.CollRecurrences, func() {
		list, err := store.Due(ctx, l.owner, mustDay(t, "2026-11-08"), 200)
		require.NoError(t, err)
		assert.Equal(t, []string{due.ID}, recIDs(list))
	})
	assert.LessOrEqual(t, op.KeysExamined, 2, "the due one only: %s", op.PlanSummary)
	assert.Contains(t, op.PlanSummary, "next_date")
}

func recIDs(list []domain.Recurrence) []string {
	out := make([]string, len(list))
	for i, r := range list {
		out[i] = r.ID
	}
	return out
}

func mustDay(t *testing.T, s string) domain.Date {
	t.Helper()
	d, err := domain.ParseDate(s)
	require.NoError(t, err)
	return d
}
