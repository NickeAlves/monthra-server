//go:build integration

package mongo_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/NickeAlves/monthra-server/internal/domain"
	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

// ledger is what transactions of one owner name. The transactions store
// never reads accounts or categories, so they need not be stored.
type ledger struct {
	owner                  string
	bank, wallet, dollars  *domain.Account
	food, salary, otherCat *domain.Category
}

func newLedger(owner string) ledger {
	account := func(currency string) *domain.Account {
		return &domain.Account{ID: bson.NewObjectID().Hex(), UserID: owner, Currency: currency}
	}
	category := func(kind domain.CategoryKind) *domain.Category {
		return &domain.Category{ID: bson.NewObjectID().Hex(), UserID: owner, Kind: kind}
	}
	return ledger{
		owner: owner, bank: account("BRL"), wallet: account("BRL"), dollars: account("USD"),
		food: category(domain.CategoryExpense), salary: category(domain.CategoryIncome), otherCat: category(domain.CategoryExpense),
	}
}

func (l ledger) spend(t *testing.T, acc *domain.Account, cat *domain.Category, amount int64, date, description string, tags ...string) domain.Transaction {
	t.Helper()
	tx, err := domain.NewTransaction(l.owner, domain.TransactionInput{
		Kind: "expense", AccountID: acc.ID, CategoryID: cat.ID, Amount: amount, Date: date, Description: description, Tags: tags,
	}, domain.TransactionRefs{Account: acc, Category: cat}, now)
	require.NoError(t, err)
	return tx
}

func (l ledger) earn(t *testing.T, acc *domain.Account, amount int64, date string) domain.Transaction {
	t.Helper()
	tx, err := domain.NewTransaction(l.owner, domain.TransactionInput{
		Kind: "income", AccountID: acc.ID, CategoryID: l.salary.ID, Amount: amount, Date: date,
	}, domain.TransactionRefs{Account: acc, Category: l.salary}, now)
	require.NoError(t, err)
	return tx
}

func (l ledger) move(t *testing.T, from, to *domain.Account, amount int64, toAmount *int64, date string) domain.Transaction {
	t.Helper()
	tx, err := domain.NewTransaction(l.owner, domain.TransactionInput{
		Kind: "transfer", AccountID: from.ID, ToAccountID: to.ID, Amount: amount, ToAmount: toAmount, Date: date,
	}, domain.TransactionRefs{Account: from, ToAccount: to}, now)
	require.NoError(t, err)
	return tx
}

func insertAll(t *testing.T, store *repo.Transactions, list ...domain.Transaction) []domain.Transaction {
	t.Helper()
	out := make([]domain.Transaction, len(list))
	for i, tx := range list {
		created, err := store.Insert(context.Background(), tx)
		require.NoError(t, err)
		out[i] = created
	}
	return out
}

func txIDs(list []domain.Transaction) []string {
	out := make([]string, len(list))
	for i, tx := range list {
		out[i] = tx.ID
	}
	return out
}

func TestTransactionsLifecycle(t *testing.T) {
	ctx := context.Background()
	store := repo.NewTransactions(testDB(t))
	l := newLedger(bson.NewObjectID().Hex())

	expense, err := store.Insert(ctx, l.spend(t, l.bank, l.food, 4590, "2026-11-08", "Café da manhã", "breakfast", "work"))
	require.NoError(t, err)
	require.Len(t, expense.ID, 24)
	got, err := store.ByID(ctx, l.owner, expense.ID)
	require.NoError(t, err)
	assert.Equal(t, expense, got, "every field round-trips")
	assert.Equal(t, "2026-11-08", got.Date.String())

	transfer, err := store.Insert(ctx, l.move(t, l.bank, l.dollars, 100000, ptr(int64(19000)), "2026-11-07"))
	require.NoError(t, err)
	got, err = store.ByID(ctx, l.owner, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, transfer, got)
	assert.Empty(t, got.CategoryID)
	assert.Equal(t, []string{}, got.Tags)

	later := now.Add(time.Hour)
	next, changed, err := domain.TransactionPatch{
		AccountID: ptr(l.wallet.ID), CategoryID: ptr(l.otherCat.ID), Amount: ptr(int64(990)), Date: ptr("2026-10-31"),
		Description: ptr(""), Tags: &[]string{"Lunch"},
	}.Apply(expense, domain.TransactionRefs{Account: l.wallet, Category: l.otherCat}, later)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, store.Update(ctx, next))
	got, err = store.ByID(ctx, l.owner, expense.ID)
	require.NoError(t, err)
	assert.Equal(t, next, got)
	assert.Equal(t, domain.TransactionExpense, got.Kind)

	next, _, err = domain.TransactionPatch{ToAccountID: ptr(l.wallet.ID)}.Apply(transfer, domain.TransactionRefs{ToAccount: l.wallet}, later)
	require.NoError(t, err)
	require.NoError(t, store.Update(ctx, next))
	got, err = store.ByID(ctx, l.owner, transfer.ID)
	require.NoError(t, err)
	assert.Equal(t, next, got)
	assert.Equal(t, int64(100000), got.ToAmount, "one currency now")

	require.NoError(t, store.Delete(ctx, l.owner, expense.ID))
	assert.ErrorIs(t, store.Delete(ctx, l.owner, expense.ID), domain.ErrNotFound)
	_, err = store.ByID(ctx, l.owner, expense.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	assert.ErrorIs(t, store.Update(ctx, expense), domain.ErrNotFound)
}

func TestTransactionsAreScopedToTheirOwner(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	store := repo.NewTransactions(db)
	l := newLedger(bson.NewObjectID().Hex())
	intruder := bson.NewObjectID().Hex()
	asOf := domain.DateOf(now)

	tx, err := store.Insert(ctx, l.spend(t, l.bank, l.food, 100, "2026-11-08", "Rent"))
	require.NoError(t, err)

	_, err = store.ByID(ctx, intruder, tx.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
	stolen := tx
	stolen.UserID, stolen.Amount = intruder, 1
	assert.ErrorIs(t, store.Update(ctx, stolen), domain.ErrNotFound)
	assert.ErrorIs(t, store.Delete(ctx, intruder, tx.ID), domain.ErrNotFound)

	list, err := store.List(ctx, intruder, domain.TransactionFilter{AccountID: l.bank.ID}, nil, 10)
	require.NoError(t, err)
	assert.Empty(t, list)
	balances, err := store.Balances(ctx, intruder, nil, asOf)
	require.NoError(t, err)
	assert.Empty(t, balances)
	balances, err = store.Balances(ctx, intruder, []string{l.bank.ID}, asOf)
	require.NoError(t, err)
	assert.Empty(t, balances)
	inUse, err := store.AccountInUse(ctx, intruder, l.bank.ID)
	require.NoError(t, err)
	assert.False(t, inUse)
	inUse, err = store.CategoryInUse(ctx, intruder, l.food.ID)
	require.NoError(t, err)
	assert.False(t, inUse)
	require.NoError(t, store.ReassignCategory(ctx, intruder, l.food.ID, l.otherCat.ID, now))

	got, err := store.ByID(ctx, l.owner, tx.ID)
	require.NoError(t, err)
	assert.Equal(t, tx, got, "untouched")

	for _, bad := range []string{"", "not-an-id"} {
		_, err = store.ByID(ctx, l.owner, bad)
		assert.ErrorIs(t, err, domain.ErrNotFound, bad)
		list, err = store.List(ctx, bad, domain.TransactionFilter{}, nil, 10)
		require.NoError(t, err)
		assert.Empty(t, list, bad)
		list, err = store.List(ctx, l.owner, domain.TransactionFilter{AccountID: bad + "x"}, nil, 10)
		require.NoError(t, err)
		assert.Empty(t, list, "a filter id that cannot exist matches nothing")
		inUse, err = store.AccountInUse(ctx, l.owner, bad)
		require.NoError(t, err)
		assert.False(t, inUse)
	}

	require.NoError(t, repo.NewUserEraser(db, repo.CollTransactions).EraseUserData(ctx, l.owner))
	list, err = store.List(ctx, l.owner, domain.TransactionFilter{}, nil, 10)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestTransactionsCursorWalksEveryOne(t *testing.T) {
	ctx := context.Background()
	store := repo.NewTransactions(testDB(t))
	l := newLedger(bson.NewObjectID().Hex())

	// 120 transactions on 12 days: ten on each day, so the id breaks ties.
	var all []domain.Transaction
	for i := range 120 {
		day := fmt.Sprintf("2026-10-%02d", 1+i%12)
		all = append(all, l.spend(t, l.bank, l.food, int64(i+1), day, ""))
	}
	all = insertAll(t, store, all...)
	want := slices.Clone(all)
	slices.SortFunc(want, func(a, b domain.Transaction) int {
		if c := b.Date.Time().Compare(a.Date.Time()); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})

	var seen []string
	var after *domain.TransactionCursor
	for pages := 0; ; pages++ {
		require.Less(t, pages, 20, "the walk ends")
		page, err := store.List(ctx, l.owner, domain.TransactionFilter{}, after, 7)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		seen = append(seen, txIDs(page)...)
		last := page[len(page)-1]
		after = &domain.TransactionCursor{Date: last.Date, ID: last.ID}
	}
	assert.Equal(t, txIDs(want), seen, "no transaction skipped or repeated, newest first")
}

func TestTransactionsFilters(t *testing.T) {
	ctx := context.Background()
	store := repo.NewTransactions(testDB(t))
	l := newLedger(bson.NewObjectID().Hex())

	list := insertAll(t, store,
		l.spend(t, l.bank, l.food, 1000, "2026-11-01", "Café da manhã", "breakfast"),
		l.spend(t, l.wallet, l.otherCat, 2000, "2026-11-02", "Padaria a.b", "breakfast", "weekend"),
		l.earn(t, l.bank, 50000, "2026-11-05"),
		l.move(t, l.bank, l.dollars, 3000, ptr(int64(600)), "2026-11-06"),
		l.move(t, l.wallet, l.bank, 4000, nil, "2026-11-07"),
	)
	coffee, bakery, salary, toDollars, fromWallet := list[0].ID, list[1].ID, list[2].ID, list[3].ID, list[4].ID
	date := func(s string) domain.Date {
		d, err := domain.ParseDate(s)
		require.NoError(t, err)
		return d
	}

	for name, tc := range map[string]struct {
		filter domain.TransactionFilter
		want   []string
	}{
		"everything":          {domain.TransactionFilter{}, []string{fromWallet, toDollars, salary, bakery, coffee}},
		"from":                {domain.TransactionFilter{From: date("2026-11-05")}, []string{fromWallet, toDollars, salary}},
		"to":                  {domain.TransactionFilter{To: date("2026-11-02")}, []string{bakery, coffee}},
		"one day":             {domain.TransactionFilter{From: date("2026-11-05"), To: date("2026-11-05")}, []string{salary}},
		"account and arrival": {domain.TransactionFilter{AccountID: l.bank.ID}, []string{fromWallet, toDollars, salary, coffee}},
		"arrival only":        {domain.TransactionFilter{AccountID: l.dollars.ID}, []string{toDollars}},
		"category":            {domain.TransactionFilter{CategoryID: l.food.ID}, []string{coffee}},
		"kind":                {domain.TransactionFilter{Kind: domain.TransactionTransfer}, []string{fromWallet, toDollars}},
		"tag":                 {domain.TransactionFilter{Tag: "breakfast"}, []string{bakery, coffee}},
		"search folded":       {domain.TransactionFilter{Search: domain.FoldText("CAFE")}, []string{coffee}},
		"search literal":      {domain.TransactionFilter{Search: "a.b"}, []string{bakery}},
		"search no pattern":   {domain.TransactionFilter{Search: "a.*"}, []string{}},
		"combined":            {domain.TransactionFilter{AccountID: l.wallet.ID, Tag: "weekend", To: date("2026-11-30")}, []string{bakery}},
		"no match":            {domain.TransactionFilter{CategoryID: bson.NewObjectID().Hex()}, []string{}},
	} {
		got, err := store.List(ctx, l.owner, tc.filter, nil, 50)
		require.NoError(t, err, name)
		assert.Equal(t, tc.want, txIDs(got), name)
	}

	// The account filter and the cursor are both alternatives ($or).
	page, err := store.List(ctx, l.owner, domain.TransactionFilter{AccountID: l.bank.ID}, nil, 2)
	require.NoError(t, err)
	require.Equal(t, []string{fromWallet, toDollars}, txIDs(page))
	page, err = store.List(ctx, l.owner, domain.TransactionFilter{AccountID: l.bank.ID}, &domain.TransactionCursor{Date: page[1].Date, ID: page[1].ID}, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{salary, coffee}, txIDs(page))
}

func TestTransactionsBalances(t *testing.T) {
	ctx := context.Background()
	store := repo.NewTransactions(testDB(t))
	l := newLedger(bson.NewObjectID().Hex())
	insertAll(t, store,
		l.spend(t, l.bank, l.food, 1000, "2026-11-01", ""),
		l.earn(t, l.bank, 5000, "2026-11-02"),
		l.move(t, l.bank, l.dollars, 2000, ptr(int64(400)), "2026-11-03"),
		l.move(t, l.dollars, l.bank, 100, ptr(int64(500)), "2026-11-04"),
		l.spend(t, l.bank, l.food, 300, "2026-11-08", "due today"),
		l.spend(t, l.bank, l.food, 700, "2026-11-20", "a bill ahead"),
	)
	asOf, err := domain.ParseDate("2026-11-08")
	require.NoError(t, err)

	all, err := store.Balances(ctx, l.owner, nil, asOf)
	require.NoError(t, err)
	assert.Equal(t, map[string]domain.Balance{
		// -1000 + 5000 - 2000 + 500 - 300; the bill ahead only in Projected.
		l.bank.ID:    {Current: 2200, Projected: 1500},
		l.dollars.ID: {Current: 300, Projected: 300},
	}, all, "an account without transactions is missing")

	some, err := store.Balances(ctx, l.owner, []string{l.dollars.ID, l.wallet.ID, "not-an-id"}, asOf)
	require.NoError(t, err)
	assert.Equal(t, map[string]domain.Balance{l.dollars.ID: {Current: 300, Projected: 300}}, some, "only the accounts asked")

	early, err := domain.ParseDate("2026-11-01")
	require.NoError(t, err)
	before, err := store.Balances(ctx, l.owner, []string{l.bank.ID}, early)
	require.NoError(t, err)
	assert.Equal(t, domain.Balance{Current: -1000, Projected: 1500}, before[l.bank.ID])

	none, err := store.Balances(ctx, l.owner, []string{}, asOf)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestTransactionsUsage(t *testing.T) {
	ctx := context.Background()
	store := repo.NewTransactions(testDB(t))
	l := newLedger(bson.NewObjectID().Hex())
	list := insertAll(t, store,
		l.spend(t, l.bank, l.food, 1000, "2026-11-01", ""),
		l.spend(t, l.bank, l.food, 2000, "2026-11-02", ""),
		l.spend(t, l.bank, l.otherCat, 3000, "2026-11-03", ""),
		l.move(t, l.bank, l.dollars, 2000, ptr(int64(400)), "2026-11-03"),
	)

	for acc, want := range map[*domain.Account]bool{l.bank: true, l.dollars: true, l.wallet: false} {
		inUse, err := store.AccountInUse(ctx, l.owner, acc.ID)
		require.NoError(t, err)
		assert.Equal(t, want, inUse, "%s (the destination of a transfer counts)", acc.Currency)
	}
	inUse, err := store.CategoryInUse(ctx, l.owner, l.food.ID)
	require.NoError(t, err)
	assert.True(t, inUse)
	inUse, err = store.CategoryInUse(ctx, l.owner, l.salary.ID)
	require.NoError(t, err)
	assert.False(t, inUse)

	at := now.Add(time.Hour)
	require.NoError(t, store.ReassignCategory(ctx, l.owner, l.food.ID, l.otherCat.ID, at))
	inUse, err = store.CategoryInUse(ctx, l.owner, l.food.ID)
	require.NoError(t, err)
	assert.False(t, inUse)
	for _, tx := range list[:2] {
		got, err := store.ByID(ctx, l.owner, tx.ID)
		require.NoError(t, err)
		assert.Equal(t, l.otherCat.ID, got.CategoryID)
		assert.Equal(t, at, got.UpdatedAt)
	}
	got, err := store.ByID(ctx, l.owner, list[2].ID)
	require.NoError(t, err)
	assert.Equal(t, now, got.UpdatedAt, "already in the target: untouched")
	assert.Error(t, store.ReassignCategory(ctx, l.owner, "bad", l.otherCat.ID, at))
}

func ptr[T any](v T) *T { return &v }
