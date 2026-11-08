package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var txNow = time.Date(2026, 11, 8, 20, 0, 0, 0, time.FixedZone("BRT", -3*3600))

// Fixtures: the records a transaction may name.
var (
	archivedAt  = txNow.Add(-time.Hour)
	txBank      = &Account{ID: "bank", Currency: "BRL"}
	txWallet    = &Account{ID: "wallet", Currency: "BRL"}
	txDollars   = &Account{ID: "dollars", Currency: "USD"}
	txOld       = &Account{ID: "old", Currency: "BRL", ArchivedAt: &archivedAt}
	txFood      = &Category{ID: "food", Kind: CategoryExpense}
	txSalary    = &Category{ID: "salary", Kind: CategoryIncome}
	expenseRefs = TransactionRefs{Account: txBank, Category: txFood}
)

func expenseInput() TransactionInput {
	return TransactionInput{
		Kind: "expense", AccountID: "bank", CategoryID: "food", Amount: 4590, Date: "2026-11-08",
		Description: "  Café da manhã  ", Tags: []string{" Breakfast", "work", "breakfast"},
	}
}

func TestNewTransaction(t *testing.T) {
	tx, err := NewTransaction("u1", expenseInput(), expenseRefs, txNow)
	require.NoError(t, err)
	assert.Equal(t, Transaction{
		UserID: "u1", Kind: TransactionExpense, AccountID: "bank", Currency: "BRL", CategoryID: "food",
		Amount: 4590, Date: DateOf(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)), Description: "Café da manhã",
		Tags: []string{"breakfast", "work"}, Source: SourceManual, CreatedAt: txNow.UTC(), UpdatedAt: txNow.UTC(),
	}, tx)

	income, err := NewTransaction("u1", TransactionInput{
		Kind: "income", AccountID: "dollars", CategoryID: "salary", Amount: MaxAmount, Date: "2026-11-05",
	}, TransactionRefs{Account: txDollars, Category: txSalary}, txNow)
	require.NoError(t, err)
	assert.Equal(t, "USD", income.Currency)
	assert.Empty(t, income.Description)
	assert.Equal(t, []string{}, income.Tags, "never nil")
	assert.False(t, income.IsTransfer())
}

func TestNewTransfer(t *testing.T) {
	transfer := func(to string, toAmount *int64, refs TransactionRefs) (Transaction, error) {
		return NewTransaction("u1", TransactionInput{
			Kind: "transfer", AccountID: "bank", ToAccountID: to, Amount: 100000, ToAmount: toAmount, Date: "2026-11-08",
		}, refs, txNow)
	}

	tx, err := transfer("wallet", nil, TransactionRefs{Account: txBank, ToAccount: txWallet})
	require.NoError(t, err)
	assert.True(t, tx.IsTransfer())
	assert.Equal(t, "BRL", tx.ToCurrency)
	assert.Equal(t, int64(100000), tx.ToAmount, "one currency: what leaves arrives")
	assert.Empty(t, tx.CategoryID)

	tx, err = transfer("wallet", ptr(int64(100000)), TransactionRefs{Account: txBank, ToAccount: txWallet})
	require.NoError(t, err, "an equal toAmount is accepted")
	assert.Equal(t, int64(100000), tx.ToAmount)

	tx, err = transfer("dollars", ptr(int64(19000)), TransactionRefs{Account: txBank, ToAccount: txDollars})
	require.NoError(t, err)
	assert.Equal(t, "USD", tx.ToCurrency)
	assert.Equal(t, int64(19000), tx.ToAmount, "two currencies: the amount received")

	for name, tc := range map[string]struct {
		to       string
		toAmount *int64
		refs     TransactionRefs
		want     map[string]string
	}{
		"no destination":         {"", nil, TransactionRefs{Account: txBank}, map[string]string{"toAccountId": CodeRequired}},
		"to itself":              {"bank", nil, TransactionRefs{Account: txBank, ToAccount: txBank}, map[string]string{"toAccountId": CodeInvalid}},
		"unknown destination":    {"ghost", nil, TransactionRefs{Account: txBank}, map[string]string{"toAccountId": CodeInvalid}},
		"archived destination":   {"old", nil, TransactionRefs{Account: txBank, ToAccount: txOld}, map[string]string{"toAccountId": CodeArchived}},
		"another amount, same":   {"wallet", ptr(int64(99000)), TransactionRefs{Account: txBank, ToAccount: txWallet}, map[string]string{"toAmount": CodeInvalid}},
		"two currencies, none":   {"dollars", nil, TransactionRefs{Account: txBank, ToAccount: txDollars}, map[string]string{"toAmount": CodeRequired}},
		"two currencies, zero":   {"dollars", ptr(int64(0)), TransactionRefs{Account: txBank, ToAccount: txDollars}, map[string]string{"toAmount": CodeOutOfRange}},
		"two currencies, beyond": {"dollars", ptr(MaxAmount + 1), TransactionRefs{Account: txBank, ToAccount: txDollars}, map[string]string{"toAmount": CodeOutOfRange}},
	} {
		_, err := transfer(tc.to, tc.toAmount, tc.refs)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}

	_, err = NewTransaction("u1", TransactionInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "wallet", CategoryID: "food", Amount: 1, Date: "2026-11-08",
	}, TransactionRefs{Account: txBank, ToAccount: txWallet, Category: txFood}, txNow)
	assert.Equal(t, map[string]string{"categoryId": CodeInvalid}, fieldsOf(t, err), "a transfer has no category")
}

func TestNewTransactionValidation(t *testing.T) {
	with := func(f func(*TransactionInput)) TransactionInput {
		in := expenseInput()
		f(&in)
		return in
	}
	for name, tc := range map[string]struct {
		in   TransactionInput
		refs TransactionRefs
		want map[string]string
	}{
		"nothing": {TransactionInput{}, TransactionRefs{}, map[string]string{"kind": CodeRequired, "amount": CodeOutOfRange, "date": CodeRequired}},
		"unknown kind": {
			with(func(in *TransactionInput) { in.Kind = "refund" }), expenseRefs, map[string]string{"kind": CodeInvalid},
		},
		"no account":       {with(func(in *TransactionInput) { in.AccountID = "" }), TransactionRefs{Category: txFood}, map[string]string{"accountId": CodeRequired}},
		"unknown account":  {with(func(in *TransactionInput) { in.AccountID = "ghost" }), TransactionRefs{Category: txFood}, map[string]string{"accountId": CodeInvalid}},
		"archived account": {with(func(in *TransactionInput) { in.AccountID = "old" }), TransactionRefs{Account: txOld, Category: txFood}, map[string]string{"accountId": CodeArchived}},
		"refs of other ids": {
			expenseInput(), TransactionRefs{Account: txWallet, Category: txSalary}, map[string]string{"accountId": CodeInvalid, "categoryId": CodeInvalid},
		},
		"no category":      {with(func(in *TransactionInput) { in.CategoryID = "" }), TransactionRefs{Account: txBank}, map[string]string{"categoryId": CodeRequired}},
		"unknown category": {with(func(in *TransactionInput) { in.CategoryID = "ghost" }), TransactionRefs{Account: txBank}, map[string]string{"categoryId": CodeInvalid}},
		"income category": {
			with(func(in *TransactionInput) { in.CategoryID = "salary" }), TransactionRefs{Account: txBank, Category: txSalary},
			map[string]string{"categoryId": CodeKindMismatch},
		},
		"transfer fields": {
			with(func(in *TransactionInput) { in.ToAccountID, in.ToAmount = "wallet", ptr(int64(1)) }), expenseRefs,
			map[string]string{"toAccountId": CodeInvalid, "toAmount": CodeInvalid},
		},
		"zero amount":     {with(func(in *TransactionInput) { in.Amount = 0 }), expenseRefs, map[string]string{"amount": CodeOutOfRange}},
		"negative amount": {with(func(in *TransactionInput) { in.Amount = -10 }), expenseRefs, map[string]string{"amount": CodeOutOfRange}},
		"huge amount":     {with(func(in *TransactionInput) { in.Amount = MaxAmount + 1 }), expenseRefs, map[string]string{"amount": CodeOutOfRange}},
		"bad date":        {with(func(in *TransactionInput) { in.Date = "2026-02-30" }), expenseRefs, map[string]string{"date": CodeInvalid}},
		"long description": {
			with(func(in *TransactionInput) { in.Description = strings.Repeat("a", MaxTransactionDescriptionLength+1) }), expenseRefs,
			map[string]string{"description": CodeTooLong},
		},
		"control character": {with(func(in *TransactionInput) { in.Description = "a\nb" }), expenseRefs, map[string]string{"description": CodeInvalid}},
		"too many tags": {
			with(func(in *TransactionInput) { in.Tags = strings.Split("a b c d e f g h i j k", " ") }), expenseRefs,
			map[string]string{"tags": CodeTooLong},
		},
		"empty tag": {with(func(in *TransactionInput) { in.Tags = []string{"ok", "  "} }), expenseRefs, map[string]string{"tags": CodeRequired}},
		"long tag": {
			with(func(in *TransactionInput) { in.Tags = []string{strings.Repeat("t", MaxTagLength+1)} }), expenseRefs,
			map[string]string{"tags": CodeTooLong},
		},
	} {
		_, err := NewTransaction("u1", tc.in, tc.refs, txNow)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}

	in := with(func(in *TransactionInput) { in.Tags = strings.Split("A a b c d e f g h i", " ") })
	tx, err := NewTransaction("u1", in, expenseRefs, txNow)
	require.NoError(t, err)
	assert.Equal(t, strings.Split("a b c d e f g h i", " "), tx.Tags, "repeats dropped, first-seen order")
}

func TestTransactionPatch(t *testing.T) {
	later := txNow.Add(time.Hour)
	current, err := NewTransaction("u1", expenseInput(), expenseRefs, txNow)
	require.NoError(t, err)

	next, changed, err := TransactionPatch{}.Apply(current, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, current, next)

	same := TransactionPatch{
		AccountID: ptr("bank"), CategoryID: ptr("food"), Amount: ptr(int64(4590)), Date: ptr("2026-11-08"),
		Description: ptr("Café da manhã "), Tags: &[]string{"BREAKFAST", "work"},
	}
	_, changed, err = same.Apply(current, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.False(t, changed, "the same values, normalized, change nothing and need no refs")

	next, changed, err = TransactionPatch{
		AccountID: ptr("dollars"), CategoryID: ptr("food"), Amount: ptr(int64(990)), Date: ptr("2026-11-01"),
		Description: ptr(""), Tags: &[]string{},
	}.Apply(current, TransactionRefs{Account: txDollars}, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "dollars", next.AccountID)
	assert.Equal(t, "USD", next.Currency, "follows the new account")
	assert.Equal(t, int64(990), next.Amount)
	assert.Equal(t, "2026-11-01", next.Date.String())
	assert.Empty(t, next.Description)
	assert.Equal(t, []string{}, next.Tags)
	assert.Equal(t, later.UTC(), next.UpdatedAt)
	assert.Equal(t, current.CreatedAt, next.CreatedAt)
	assert.Equal(t, []string{"breakfast", "work"}, current.Tags, "current is never modified")

	// A transaction of an account archived since can still be corrected.
	inOld := current
	inOld.AccountID = "old"
	_, changed, err = TransactionPatch{Amount: ptr(int64(1))}.Apply(inOld, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.True(t, changed)

	for name, tc := range map[string]struct {
		patch TransactionPatch
		refs  TransactionRefs
		want  map[string]string
	}{
		"to an archived account": {TransactionPatch{AccountID: ptr("old")}, TransactionRefs{Account: txOld}, map[string]string{"accountId": CodeArchived}},
		"to an unknown account":  {TransactionPatch{AccountID: ptr("ghost")}, TransactionRefs{}, map[string]string{"accountId": CodeInvalid}},
		"account cleared":        {TransactionPatch{AccountID: ptr("")}, TransactionRefs{}, map[string]string{"accountId": CodeRequired}},
		"category cleared":       {TransactionPatch{CategoryID: ptr("")}, TransactionRefs{}, map[string]string{"categoryId": CodeRequired}},
		"income category": {
			TransactionPatch{CategoryID: ptr("salary")}, TransactionRefs{Category: txSalary}, map[string]string{"categoryId": CodeKindMismatch},
		},
		"transfer fields": {
			TransactionPatch{ToAccountID: ptr("wallet"), ToAmount: ptr(int64(1))}, TransactionRefs{ToAccount: txWallet},
			map[string]string{"toAccountId": CodeInvalid, "toAmount": CodeInvalid},
		},
		"bad values": {
			TransactionPatch{Amount: ptr(int64(0)), Date: ptr("tomorrow"), Description: ptr("\u202e"), Tags: &[]string{""}}, TransactionRefs{},
			map[string]string{"amount": CodeOutOfRange, "date": CodeInvalid, "description": CodeInvalid, "tags": CodeRequired},
		},
	} {
		_, _, err := tc.patch.Apply(current, tc.refs, later)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}
}

func TestTransferPatch(t *testing.T) {
	later := txNow.Add(time.Hour)
	local, err := NewTransaction("u1", TransactionInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "wallet", Amount: 5000, Date: "2026-11-08",
	}, TransactionRefs{Account: txBank, ToAccount: txWallet}, txNow)
	require.NoError(t, err)

	next, _, err := TransactionPatch{Amount: ptr(int64(7000))}.Apply(local, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.Equal(t, int64(7000), next.ToAmount, "one currency: the amount received follows")

	_, _, err = TransactionPatch{ToAccountID: ptr("dollars")}.Apply(local, TransactionRefs{ToAccount: txDollars}, later)
	assert.Equal(t, map[string]string{"toAmount": CodeRequired}, fieldsOf(t, err), "now two currencies: say how much arrived")

	cross, _, err := TransactionPatch{ToAccountID: ptr("dollars"), ToAmount: ptr(int64(900))}.Apply(local, TransactionRefs{ToAccount: txDollars}, later)
	require.NoError(t, err)
	assert.Equal(t, "USD", cross.ToCurrency)
	assert.Equal(t, int64(900), cross.ToAmount)

	next, changed, err := TransactionPatch{Amount: ptr(int64(6000))}.Apply(cross, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, int64(900), next.ToAmount, "still two currencies: the amount received stays")

	back, _, err := TransactionPatch{ToAccountID: ptr("wallet")}.Apply(cross, TransactionRefs{ToAccount: txWallet}, later)
	require.NoError(t, err)
	assert.Equal(t, int64(5000), back.ToAmount, "one currency again: equal to the amount sent")

	swapped, _, err := TransactionPatch{AccountID: ptr("wallet"), ToAccountID: ptr("bank")}.Apply(local, TransactionRefs{Account: txWallet, ToAccount: txBank}, later)
	require.NoError(t, err)
	assert.Equal(t, "wallet", swapped.AccountID)
	assert.Equal(t, "bank", swapped.ToAccountID)

	_, _, err = TransactionPatch{CategoryID: ptr("")}.Apply(local, TransactionRefs{}, later)
	require.NoError(t, err, "clearing the absent category of a transfer is harmless")

	for name, tc := range map[string]struct {
		patch TransactionPatch
		refs  TransactionRefs
		want  map[string]string
	}{
		"to itself":        {TransactionPatch{ToAccountID: ptr("bank")}, TransactionRefs{ToAccount: txBank}, map[string]string{"toAccountId": CodeInvalid}},
		"from the target":  {TransactionPatch{AccountID: ptr("wallet")}, TransactionRefs{Account: txWallet}, map[string]string{"toAccountId": CodeInvalid}},
		"with a category":  {TransactionPatch{CategoryID: ptr("food")}, TransactionRefs{Category: txFood}, map[string]string{"categoryId": CodeInvalid}},
		"another amount":   {TransactionPatch{ToAmount: ptr(int64(1))}, TransactionRefs{}, map[string]string{"toAmount": CodeInvalid}},
		"to archived":      {TransactionPatch{ToAccountID: ptr("old")}, TransactionRefs{ToAccount: txOld}, map[string]string{"toAccountId": CodeArchived}},
		"cleared receiver": {TransactionPatch{ToAccountID: ptr("")}, TransactionRefs{}, map[string]string{"toAccountId": CodeRequired}},
	} {
		_, _, err := tc.patch.Apply(local, tc.refs, later)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}
}

func TestFoldText(t *testing.T) {
	for in, want := range map[string]string{
		"Café Açaí":          "cafe acai",
		"PÃO DE QUEIJO":      "pao de queijo",
		"Ñandú":              "nandu",
		"uber *trip":         "uber *trip",
		"":                   "",
		"e\u0301 (combined)": "e (combined)",
	} {
		assert.Equal(t, want, FoldText(in), in)
	}
}

func TestNormalizeFilters(t *testing.T) {
	tag, err := NormalizeTag("  Viagem ")
	require.NoError(t, err)
	assert.Equal(t, "viagem", tag)
	_, err = NormalizeTag(" ")
	assert.Equal(t, CodeRequired, codeOf(t, err))

	q, err := NormalizeSearch("  Café ")
	require.NoError(t, err)
	assert.Equal(t, "cafe", q)
	q, err = NormalizeSearch("   ")
	require.NoError(t, err)
	assert.Empty(t, q, "blank is no search")
	_, err = NormalizeSearch(strings.Repeat("a", MaxSearchLength+1))
	assert.Equal(t, CodeTooLong, codeOf(t, err))
	_, err = NormalizeSearch("a\x00b")
	assert.Equal(t, CodeInvalid, codeOf(t, err))
}
