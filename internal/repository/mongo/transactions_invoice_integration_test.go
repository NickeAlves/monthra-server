//go:build integration

package mongo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/NickeAlves/monthra-server/internal/domain"
	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

// newCard is a credit card of owner that closes on the 25th and falls due
// on the 5th. The transactions store never reads it.
func newCard(owner string) *domain.Account {
	return &domain.Account{
		ID: bson.NewObjectID().Hex(), UserID: owner, Type: domain.AccountCreditCard, Currency: "BRL",
		Card: &domain.CardSettings{ClosingDay: 25, DueDay: 5},
	}
}

func mustMonth(t *testing.T, s string) domain.Month {
	t.Helper()
	m, err := domain.ParseMonth(s)
	require.NoError(t, err)
	return m
}

func TestTransactionsInvoiceSums(t *testing.T) {
	ctx := context.Background()
	store := repo.NewTransactions(testDB(t))
	l := newLedger(bson.NewObjectID().Hex())
	card := newCard(l.owner)
	insertAll(t, store,
		l.spend(t, card, l.food, 1000, "2026-11-24", "december"),
		l.spend(t, card, l.food, 2000, "2026-11-25", "january: the closing day"),
		l.earn(t, card, 300, "2026-11-10"),
		l.move(t, l.bank, card, 5000, nil, "2026-12-05"),   // pays December
		l.move(t, card, l.bank, 700, nil, "2026-12-26"),    // a withdrawal in February
		l.spend(t, l.bank, l.food, 9999, "2026-11-24", ""), // no card
	)
	// Another owner's transaction naming the same card id stays theirs.
	other := newLedger(bson.NewObjectID().Hex())
	insertAll(t, store, other.spend(t, card, other.food, 123, "2026-11-24", ""))

	sums, err := store.InvoiceSums(ctx, l.owner, card.ID, mustMonth(t, "2027-01"))
	require.NoError(t, err)
	assert.Equal(t, []domain.InvoiceSums{
		{Month: mustMonth(t, "2026-12"), Charges: 1000, Credits: 300, Payments: 5000},
		{Month: mustMonth(t, "2027-01"), Charges: 2000},
	}, sums, "oldest first, up to the month asked")

	sums, err = store.InvoiceSums(ctx, l.owner, card.ID, mustMonth(t, "2027-02"))
	require.NoError(t, err)
	require.Len(t, sums, 3)
	assert.Equal(t, domain.InvoiceSums{Month: mustMonth(t, "2027-02"), Charges: 700}, sums[2])

	theirs, err := store.InvoiceSums(ctx, other.owner, card.ID, mustMonth(t, "2027-12"))
	require.NoError(t, err)
	assert.Equal(t, []domain.InvoiceSums{{Month: mustMonth(t, "2026-12"), Charges: 123}}, theirs)

	for _, ids := range [][2]string{{"not-an-id", card.ID}, {l.owner, "not-an-id"}} {
		none, err := store.InvoiceSums(ctx, ids[0], ids[1], mustMonth(t, "2027-12"))
		require.NoError(t, err)
		assert.Empty(t, none)
	}
}

func TestTransactionsInvoiceFilter(t *testing.T) {
	ctx := context.Background()
	store := repo.NewTransactions(testDB(t))
	l := newLedger(bson.NewObjectID().Hex())
	card := newCard(l.owner)
	created := insertAll(t, store,
		l.spend(t, card, l.food, 1000, "2026-11-24", ""),
		l.earn(t, card, 300, "2026-11-10"),
		l.move(t, l.bank, card, 5000, nil, "2026-12-05"),
		l.spend(t, card, l.food, 2000, "2026-11-25", ""), // January
	)
	dec := mustMonth(t, "2026-12")

	got, err := store.List(ctx, l.owner, domain.TransactionFilter{AccountID: card.ID, Invoice: dec}, nil, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{created[2].ID, created[0].ID, created[1].ID}, txIDs(got), "charges, credits and the payment received")
	assert.Equal(t, dec, got[0].ToInvoiceMonth)
	assert.True(t, got[0].InvoiceMonth.IsZero(), "the bank leg has none")
	assert.Equal(t, dec, got[1].InvoiceMonth)

	got, err = store.List(ctx, l.owner, domain.TransactionFilter{AccountID: l.bank.ID, Invoice: dec}, nil, 10)
	require.NoError(t, err)
	assert.Empty(t, got, "the bank has no invoice")

	got, err = store.List(ctx, l.owner, domain.TransactionFilter{Invoice: dec}, nil, 10)
	require.NoError(t, err)
	assert.Len(t, got, 3, "without an account, the legs of every card in that invoice")
}

func TestTransactionsInvoiceIsRemovedOffTheCard(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	store := repo.NewTransactions(db)
	l := newLedger(bson.NewObjectID().Hex())
	card := newCard(l.owner)
	purchase := insertAll(t, store, l.spend(t, card, l.food, 1000, "2026-11-24", ""))[0]
	payment := insertAll(t, store, l.move(t, l.bank, card, 5000, nil, "2026-12-05"))[0]

	raw := func(id string) bson.M {
		t.Helper()
		oid, err := bson.ObjectIDFromHex(id)
		require.NoError(t, err)
		var doc bson.M
		require.NoError(t, db.Collection(repo.CollTransactions).FindOne(ctx, bson.D{{Key: "_id", Value: oid}}).Decode(&doc))
		return doc
	}
	assert.Equal(t, "2026-12", raw(purchase.ID)["invoice_month"])
	assert.NotContains(t, raw(payment.ID), "invoice_month", "absent, never null")
	assert.Equal(t, "2026-12", raw(payment.ID)["to_invoice_month"])

	next, changed, err := domain.TransactionPatch{AccountID: &l.bank.ID}.Apply(purchase, domain.TransactionRefs{Account: l.bank}, now)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, store.Update(ctx, next))
	assert.NotContains(t, raw(purchase.ID), "invoice_month")

	back, _, err := domain.TransactionPatch{AccountID: &card.ID}.Apply(next, domain.TransactionRefs{Account: card}, now)
	require.NoError(t, err)
	require.NoError(t, store.Update(ctx, back))
	assert.Equal(t, "2026-12", raw(purchase.ID)["invoice_month"])

	wallet := l.wallet.ID
	moved, _, err := domain.TransactionPatch{ToAccountID: &wallet}.Apply(payment, domain.TransactionRefs{ToAccount: l.wallet}, now)
	require.NoError(t, err)
	require.NoError(t, store.Update(ctx, moved))
	assert.NotContains(t, raw(payment.ID), "to_invoice_month")
	stored, err := store.ByID(ctx, l.owner, payment.ID)
	require.NoError(t, err)
	assert.True(t, stored.ToInvoiceMonth.IsZero())
}

// profiledOp is what the MongoDB profiler recorded of one operation.
type profiledOp struct {
	Op           string `bson:"op"`
	KeysExamined int    `bson:"keysExamined"`
	DocsExamined int    `bson:"docsExamined"`
	NReturned    int    `bson:"nreturned"`
	PlanSummary  string `bson:"planSummary"`
}

// profile runs fn with the profiler on and returns the one operation it ran
// on the transactions collection, so a test can tell how much it read.
func profile(t *testing.T, db *mongo.Database, fn func()) profiledOp {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, db.RunCommand(ctx, bson.D{{Key: "profile", Value: 0}}).Err())
	require.NoError(t, db.Collection("system.profile").Drop(ctx))
	require.NoError(t, db.RunCommand(ctx, bson.D{{Key: "profile", Value: 2}}).Err())
	fn()
	require.NoError(t, db.RunCommand(ctx, bson.D{{Key: "profile", Value: 0}}).Err())
	cur, err := db.Collection("system.profile").Find(ctx, bson.D{{Key: "ns", Value: db.Name() + "." + repo.CollTransactions}})
	require.NoError(t, err)
	var ops []profiledOp
	require.NoError(t, cur.All(ctx, &ops))
	require.Len(t, ops, 1)
	return ops[0]
}

func TestTransactionsQueriesReadOnlyTheirEntries(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	store := repo.NewTransactions(db)
	l := newLedger(bson.NewObjectID().Hex())
	card := newCard(l.owner)
	// 40 months of a card, its payments and a wallet: reading one account,
	// one invoice or one page must not read the rest.
	var history []domain.Transaction
	for i := range 40 {
		date := mustMonth(t, "2024-01").AddMonths(i).Day(10).String()
		history = append(history,
			l.spend(t, card, l.food, 100, date, ""),
			l.move(t, l.bank, card, 100, nil, date),
			l.spend(t, l.wallet, l.food, 100, date, ""))
	}
	insertAll(t, store, history...)

	var page []domain.Transaction
	op := profile(t, db, func() {
		var err error
		page, err = store.List(ctx, l.owner, domain.TransactionFilter{AccountID: card.ID, Invoice: mustMonth(t, "2025-06")}, nil, 51)
		require.NoError(t, err)
	})
	require.Len(t, page, 2, "May's purchase and June's payment")
	assert.LessOrEqual(t, op.KeysExamined, 3, "an invoice reads its own entries: %s", op.PlanSummary)
	assert.Contains(t, op.PlanSummary, "invoice_month")
	assert.NotContains(t, op.PlanSummary, "SORT {", "the indexes give the order")

	op = profile(t, db, func() {
		sums, err := store.InvoiceSums(ctx, l.owner, card.ID, mustMonth(t, "2024-06"))
		require.NoError(t, err)
		require.Len(t, sums, 6)
	})
	// Five purchases (January to May, in the invoices of February to June)
	// and six payments.
	assert.LessOrEqual(t, op.KeysExamined, 12, "the sums read the invoices asked for: %s", op.PlanSummary)

	op = profile(t, db, func() {
		var err error
		page, err = store.List(ctx, l.owner, domain.TransactionFilter{AccountID: l.wallet.ID}, nil, 10)
		require.NoError(t, err)
	})
	require.Len(t, page, 10)
	assert.LessOrEqual(t, op.KeysExamined, 11, "a statement reads its account only: %s", op.PlanSummary)

	last := page[len(page)-1]
	op = profile(t, db, func() {
		next, err := store.List(ctx, l.owner, domain.TransactionFilter{AccountID: l.wallet.ID},
			&domain.TransactionCursor{Date: last.Date, ID: last.ID}, 10)
		require.NoError(t, err)
		require.Len(t, next, 10)
		assert.True(t, next[0].Date.Before(last.Date))
	})
	assert.LessOrEqual(t, op.KeysExamined, 12, "the next page starts where the last one ended: %s", op.PlanSummary)

	op = profile(t, db, func() {
		next, err := store.List(ctx, l.owner, domain.TransactionFilter{}, &domain.TransactionCursor{Date: last.Date, ID: last.ID}, 10)
		require.NoError(t, err)
		require.Len(t, next, 10)
	})
	assert.LessOrEqual(t, op.KeysExamined, 14, "without filters too: %s", op.PlanSummary)

	op = profile(t, db, func() {
		sums, err := store.Balances(ctx, l.owner, []string{l.wallet.ID}, mustMonth(t, "2030-01").Day(1))
		require.NoError(t, err)
		assert.Equal(t, int64(-4000), sums[l.wallet.ID].Projected)
	})
	assert.LessOrEqual(t, op.KeysExamined, 41, "one balance reads its account only: %s", op.PlanSummary)

	op = profile(t, db, func() {
		used, err := store.AccountInUse(ctx, l.owner, l.dollars.ID)
		require.NoError(t, err)
		assert.False(t, used)
	})
	assert.LessOrEqual(t, op.KeysExamined, 1, "an unused account reads nothing: %s", op.PlanSummary)
}
