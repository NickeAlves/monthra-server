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

// buy stores a purchase of acc in n installments and returns them.
func (l ledger) buy(t *testing.T, store *repo.Transactions, acc *domain.Account, n int, amount int64, date string) []domain.Transaction {
	t.Helper()
	ts, err := domain.NewTransactions(l.owner, domain.TransactionInput{
		Kind: "expense", AccountID: acc.ID, CategoryID: l.food.ID, Amount: amount, Date: date, Installments: n,
	}, domain.TransactionRefs{Account: acc, Category: l.food}, now)
	require.NoError(t, err)
	created, err := store.InsertInstallments(context.Background(), ts)
	require.NoError(t, err)
	return created
}

func numbers(list []domain.Transaction) []int {
	out := make([]int, len(list))
	for i, tx := range list {
		out[i] = tx.Installment.Number
	}
	return out
}

func TestTransactionsInstallmentGroups(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	store := repo.NewTransactions(db)
	l := newLedger(bson.NewObjectID().Hex())
	card := newCard(l.owner)
	sofa := l.buy(t, store, card, 3, 100000, "2026-11-24")
	tv := l.buy(t, store, l.bank, 2, 5000, "2026-11-08")
	insertAll(t, store, l.spend(t, card, l.food, 100, "2026-11-24", "not an installment"))

	group := sofa[0].Installment.GroupID
	require.NotEmpty(t, group)
	for i, inst := range sofa {
		assert.Equal(t, domain.Installment{GroupID: group, Number: i + 1, Count: 3}, *inst.Installment)
		stored, err := store.ByID(ctx, l.owner, inst.ID)
		require.NoError(t, err)
		assert.Equal(t, inst, stored)
	}
	assert.NotEqual(t, group, tv[0].Installment.GroupID, "a group per purchase")
	assert.Equal(t, []string{"2026-12", "2027-01", "2027-02"}, []string{
		sofa[0].InvoiceMonth.String(), sofa[1].InvoiceMonth.String(), sofa[2].InvoiceMonth.String(),
	})

	all, err := store.Installments(ctx, l.owner, group, 1)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3}, numbers(all))
	later, err := store.Installments(ctx, l.owner, group, 2)
	require.NoError(t, err)
	assert.Equal(t, []int{2, 3}, numbers(later))

	listed, err := store.List(ctx, l.owner, domain.TransactionFilter{InstallmentGroupID: group}, nil, 50)
	require.NoError(t, err)
	assert.Equal(t, []string{sofa[2].ID, sofa[1].ID, sofa[0].ID}, txIDs(listed), "newest first")
	listed, err = store.List(ctx, l.owner, domain.TransactionFilter{InstallmentGroupID: group, AccountID: card.ID}, nil, 50)
	require.NoError(t, err)
	assert.Len(t, listed, 3, "with the account of the purchase")
	listed, err = store.List(ctx, l.owner, domain.TransactionFilter{InstallmentGroupID: group, AccountID: l.bank.ID}, nil, 50)
	require.NoError(t, err)
	assert.Empty(t, listed)
	listed, err = store.List(ctx, l.owner, domain.TransactionFilter{InstallmentGroupID: "not-an-id"}, nil, 50)
	require.NoError(t, err)
	assert.Empty(t, listed)

	// Moving the whole purchase off the card removes its invoices.
	toBank := l.bank.ID
	edit, err := domain.TransactionPatch{AccountID: &toBank}.ApplyInScope(sofa[1], domain.ScopeAll,
		[]domain.Transaction{sofa[0], sofa[2]}, domain.TransactionRefs{Account: l.bank}, now)
	require.NoError(t, err)
	require.Len(t, edit.Others, 2)
	require.NoError(t, store.UpdateMany(ctx, append(edit.Others, edit.Next)))
	require.NoError(t, store.UpdateMany(ctx, nil))
	for _, inst := range sofa {
		var doc bson.M
		oid, err := bson.ObjectIDFromHex(inst.ID)
		require.NoError(t, err)
		require.NoError(t, db.Collection(repo.CollTransactions).FindOne(ctx, bson.D{{Key: "_id", Value: oid}}).Decode(&doc))
		assert.NotContains(t, doc, "invoice_month")
		stored, err := store.ByID(ctx, l.owner, inst.ID)
		require.NoError(t, err)
		assert.Equal(t, l.bank.ID, stored.AccountID)
		assert.Equal(t, inst.Installment, stored.Installment, "the installment never changes")
	}

	require.NoError(t, store.DeleteInstallments(ctx, l.owner, group, 2))
	left, err := store.Installments(ctx, l.owner, group, 1)
	require.NoError(t, err)
	assert.Equal(t, []int{1}, numbers(left))
	require.NoError(t, store.DeleteInstallments(ctx, l.owner, group, 2), "again, a no-op")
	require.NoError(t, store.DeleteInstallments(ctx, l.owner, group, 1))
	left, err = store.Installments(ctx, l.owner, group, 1)
	require.NoError(t, err)
	assert.Empty(t, left)
	tvLeft, err := store.Installments(ctx, l.owner, tv[0].Installment.GroupID, 1)
	require.NoError(t, err)
	assert.Len(t, tvLeft, 2, "other purchases stay")

	_, err = store.InsertInstallments(ctx, []domain.Transaction{l.spend(t, l.bank, l.food, 1, "2026-11-08", "")})
	assert.Error(t, err, "only installments")
	for _, ids := range [][2]string{{"not-an-id", group}, {l.owner, "not-an-id"}} {
		none, err := store.Installments(ctx, ids[0], ids[1], 1)
		require.NoError(t, err)
		assert.Empty(t, none)
		assert.NoError(t, store.DeleteInstallments(ctx, ids[0], ids[1], 1))
	}
}

func TestTransactionsInstallmentsAreScopedToTheirOwner(t *testing.T) {
	ctx := context.Background()
	store := repo.NewTransactions(testDB(t))
	a := newLedger(bson.NewObjectID().Hex())
	b := newLedger(bson.NewObjectID().Hex())
	theirs := a.buy(t, store, a.bank, 4, 4000, "2026-11-08")
	group := theirs[0].Installment.GroupID

	none, err := store.Installments(ctx, b.owner, group, 1)
	require.NoError(t, err)
	assert.Empty(t, none)
	listed, err := store.List(ctx, b.owner, domain.TransactionFilter{InstallmentGroupID: group}, nil, 50)
	require.NoError(t, err)
	assert.Empty(t, listed)

	require.NoError(t, store.DeleteInstallments(ctx, b.owner, group, 1))
	forged := theirs[0]
	forged.UserID, forged.Amount = b.owner, 1
	require.NoError(t, store.UpdateMany(ctx, []domain.Transaction{forged}), "matches nothing")

	kept, err := store.Installments(ctx, a.owner, group, 1)
	require.NoError(t, err)
	assert.Equal(t, theirs, kept, "B never reaches A's purchase")
}

func TestInstallmentQueriesReadOnlyTheirGroup(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	store := repo.NewTransactions(db)
	l := newLedger(bson.NewObjectID().Hex())
	card := newCard(l.owner)
	var history []domain.Transaction
	for i := range 40 {
		history = append(history, l.spend(t, card, l.food, 100, mustMonth(t, "2024-01").AddMonths(i).Day(10).String(), ""))
	}
	insertAll(t, store, history...)
	l.buy(t, store, card, 12, 120000, "2025-01-15")
	phone := l.buy(t, store, card, 12, 240000, "2025-03-20")
	group := phone[0].Installment.GroupID

	op := profile(t, db, func() {
		listed, err := store.List(ctx, l.owner, domain.TransactionFilter{InstallmentGroupID: group}, nil, 51)
		require.NoError(t, err)
		require.Len(t, listed, 12)
	})
	assert.LessOrEqual(t, op.KeysExamined, 12, "a purchase reads its own installments: %s", op.PlanSummary)
	assert.Contains(t, op.PlanSummary, "installment.group_id")
	assert.NotContains(t, op.PlanSummary, "SORT {", "the index gives the order")

	op = profile(t, db, func() {
		later, err := store.Installments(ctx, l.owner, group, 6)
		require.NoError(t, err)
		assert.Equal(t, []int{6, 7, 8, 9, 10, 11, 12}, numbers(later))
	})
	assert.LessOrEqual(t, op.KeysExamined, 12, "a scope reads its group only: %s", op.PlanSummary)

	op = profile(t, db, func() {
		require.NoError(t, store.DeleteInstallments(ctx, l.owner, group, 10))
	})
	assert.LessOrEqual(t, op.KeysExamined, 12, "and so does a deletion: %s", op.PlanSummary)
}
