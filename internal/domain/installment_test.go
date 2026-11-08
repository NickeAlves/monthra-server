package domain

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func purchaseIn(n int, date string, amount int64) TransactionInput {
	in := expenseInput()
	in.Installments, in.Date, in.Amount = n, date, amount
	return in
}

// group returns the installments of a purchase as the store holds them:
// with ids ("i1", "i2", ...) and group "g".
func group(t *testing.T, in TransactionInput, refs TransactionRefs) []Transaction {
	t.Helper()
	ts, err := NewTransactions("u1", in, refs, txNow)
	require.NoError(t, err)
	for i := range ts {
		ts[i].ID = fmt.Sprintf("i%d", i+1)
		ts[i].Installment.GroupID = "g"
	}
	return ts
}

func amounts(ts []Transaction) []int64 {
	out := make([]int64, len(ts))
	for i, t := range ts {
		out[i] = t.Amount
	}
	return out
}

func dates(ts []Transaction) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Date.String()
	}
	return out
}

func invoices(ts []Transaction) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.InvoiceMonth.String()
	}
	return out
}

func TestNewTransactionsSplitsThePurchase(t *testing.T) {
	ts, err := NewTransactions("u1", purchaseIn(3, "2027-01-31", 100000), expenseRefs, txNow)
	require.NoError(t, err)
	require.Len(t, ts, 3)
	assert.Equal(t, []int64{33334, 33333, 33333}, amounts(ts), "the remainder goes to the first")
	assert.Equal(t, []string{"2027-01-31", "2027-02-28", "2027-03-31"}, dates(ts), "from the day of the purchase")
	for i, inst := range ts {
		assert.Equal(t, &Installment{Number: i + 1, Count: 3}, inst.Installment, "the store assigns the group")
		assert.Equal(t, "Café da manhã", inst.Description)
		assert.Equal(t, "food", inst.CategoryID)
		assert.True(t, inst.InvoiceMonth.IsZero(), "off a card")
	}
	ts[0].Tags[0] = "changed"
	assert.Equal(t, "breakfast", ts[1].Tags[0], "each installment holds its own tags")

	leap, err := NewTransactions("u1", purchaseIn(2, "2028-01-31", 2), expenseRefs, txNow)
	require.NoError(t, err)
	assert.Equal(t, []string{"2028-01-31", "2028-02-29"}, dates(leap))
	assert.Equal(t, []int64{1, 1}, amounts(leap))

	most, err := NewTransactions("u1", purchaseIn(MaxInstallments, "2097-01-15", 4800), expenseRefs, txNow)
	require.NoError(t, err)
	assert.Equal(t, "2100-12-15", most[MaxInstallments-1].Date.String())

	one, err := NewTransactions("u1", expenseInput(), expenseRefs, txNow)
	require.NoError(t, err)
	require.Len(t, one, 1)
	assert.Nil(t, one[0].Installment)
}

func TestNewTransactionsOnACard(t *testing.T) {
	refs := TransactionRefs{Account: txCard, Category: txFood}
	in := cardPurchase("2026-11-24", "")
	in.Installments = 3
	ts, err := NewTransactions("u1", in, refs, txNow)
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-12", "2027-01", "2027-02"}, invoices(ts), "one installment per invoice")

	in.Date = "2026-11-25"
	ts, err = NewTransactions("u1", in, refs, txNow)
	require.NoError(t, err)
	assert.Equal(t, []string{"2027-01", "2027-02", "2027-03"}, invoices(ts), "bought on the closing day")

	in.Date, in.InvoiceMonth = "2026-11-24", "2027-01"
	ts, err = NewTransactions("u1", in, refs, txNow)
	require.NoError(t, err)
	assert.Equal(t, []string{"2027-01", "2027-02", "2027-03"}, invoices(ts), "the first invoice the owner named")
}

func TestNewTransactionsValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		in   TransactionInput
		refs TransactionRefs
		want map[string]string
	}{
		"one":          {purchaseIn(1, "2026-11-08", 1000), expenseRefs, map[string]string{"installments": CodeOutOfRange}},
		"negative":     {purchaseIn(-2, "2026-11-08", 1000), expenseRefs, map[string]string{"installments": CodeOutOfRange}},
		"too many":     {purchaseIn(MaxInstallments+1, "2026-11-08", 100000), expenseRefs, map[string]string{"installments": CodeOutOfRange}},
		"cents short":  {purchaseIn(12, "2026-11-08", 5), expenseRefs, map[string]string{"installments": CodeOutOfRange}},
		"after 2100":   {purchaseIn(12, "2100-06-01", 1200), expenseRefs, map[string]string{"installments": CodeOutOfRange}},
		"bad amount":   {purchaseIn(3, "2026-11-08", 0), expenseRefs, map[string]string{"amount": CodeOutOfRange}},
		"bad date too": {purchaseIn(3, "2026-13-01", 1000), expenseRefs, map[string]string{"date": CodeInvalid}},
		"income": {
			TransactionInput{Kind: "income", AccountID: "bank", CategoryID: "salary", Amount: 1000, Date: "2026-11-08", Installments: 2},
			TransactionRefs{Account: txBank, Category: txSalary}, map[string]string{"installments": CodeInvalid},
		},
		"transfer": {
			TransactionInput{Kind: "transfer", AccountID: "bank", ToAccountID: "wallet", Amount: 1000, Date: "2026-11-08", Installments: 2},
			TransactionRefs{Account: txBank, ToAccount: txWallet}, map[string]string{"installments": CodeInvalid},
		},
	} {
		_, err := NewTransactions("u1", tc.in, tc.refs, txNow)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}
}

func TestApplyInScopeShiftsTheDays(t *testing.T) {
	in := cardPurchase("2026-11-24", "")
	in.Installments, in.Amount = 4, 100001
	ts := group(t, in, TransactionRefs{Account: txCard, Category: txFood})
	require.Equal(t, []string{"2026-12", "2027-01", "2027-02", "2027-03"}, invoices(ts))
	later := txNow.Add(time.Hour)

	// A day after the closing: this installment and the next ones move to
	// the next invoice.
	next, changed, err := TransactionPatch{Date: ptr("2026-12-26")}.ApplyInScope(ts[1], ScopeFuture, ts[2:],
		TransactionRefs{Account: txCard}, later)
	require.NoError(t, err)
	assert.Equal(t, "2026-12-26", next.Date.String())
	assert.Equal(t, []string{"2026-12-26", "2027-01-26", "2027-02-26"}, dates(changed))
	assert.Equal(t, []string{"2027-02", "2027-03", "2027-04"}, invoices(changed))
	assert.Equal(t, []string{"i2", "i3", "i4"}, []string{changed[0].ID, changed[1].ID, changed[2].ID})
	for _, c := range changed {
		assert.Equal(t, later.UTC(), c.UpdatedAt)
		assert.Equal(t, int64(25000), c.Amount, "the amount stays")
	}

	// With every installment, those before move too; within the cycle, the
	// invoices stay.
	_, changed, err = TransactionPatch{Date: ptr("2026-12-10")}.ApplyInScope(ts[1], ScopeAll, []Transaction{ts[0], ts[2], ts[3]},
		TransactionRefs{Account: txCard}, later)
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-12-10", "2026-11-10", "2027-01-10", "2027-02-10"}, dates(changed))
	assert.Equal(t, []string{"2027-01", "2026-12", "2027-02", "2027-03"}, invoices(changed))

	// Naming the invoice moves the invoices only.
	_, changed, err = TransactionPatch{InvoiceMonth: ptr("2027-02")}.ApplyInScope(ts[1], ScopeFuture, ts[2:],
		TransactionRefs{Account: txCard}, later)
	require.NoError(t, err)
	assert.Equal(t, []string{"2027-02", "2027-03", "2027-04"}, invoices(changed))
	assert.Equal(t, []string{"2026-12-24", "2027-01-24", "2027-02-24"}, dates(changed))
}

func TestApplyInScopeCopiesTheChanges(t *testing.T) {
	ts := group(t, purchaseIn(3, "2027-01-31", 100000), expenseRefs)
	others := []Transaction{ts[0], ts[2]}

	_, changed, err := TransactionPatch{Amount: ptr(int64(40000))}.ApplyInScope(ts[1], ScopeAll, others, TransactionRefs{}, txNow)
	require.NoError(t, err)
	assert.Equal(t, []int64{40000, 40000, 40000}, amounts(changed), "the amount of each installment")

	// A whole form where only the description changed: the other fields of
	// each installment stay, the remainder of the first and the last day of
	// the month included.
	form := TransactionPatch{
		AccountID: ptr("bank"), CategoryID: ptr("food"), Amount: ptr(int64(33333)), Date: ptr("2027-02-28"),
		Description: ptr("Sofa"), Tags: ptr([]string{"breakfast", "work"}),
	}
	next, changed, err := form.ApplyInScope(ts[1], ScopeAll, others, TransactionRefs{}, txNow)
	require.NoError(t, err)
	assert.Equal(t, "Sofa", next.Description)
	require.Len(t, changed, 3)
	assert.Equal(t, []int64{33333, 33334, 33333}, amounts(changed))
	assert.Equal(t, []string{"2027-02-28", "2027-01-31", "2027-03-31"}, dates(changed))
	for _, c := range changed {
		assert.Equal(t, "Sofa", c.Description)
	}

	_, changed, err = TransactionPatch{
		CategoryID: ptr("rent"), Tags: ptr([]string{"home"}),
	}.ApplyInScope(ts[1], ScopeFuture, ts[2:], TransactionRefs{Category: &Category{ID: "rent", Kind: CategoryExpense}}, txNow)
	require.NoError(t, err)
	require.Len(t, changed, 2)
	assert.Equal(t, "rent", changed[1].CategoryID)
	assert.Equal(t, []string{"home"}, changed[1].Tags)

	_, changed, err = TransactionPatch{Description: ptr("Café da manhã")}.ApplyInScope(ts[1], ScopeAll, others, TransactionRefs{}, txNow)
	require.NoError(t, err)
	assert.Empty(t, changed, "nothing changed")

	_, changed, err = TransactionPatch{Amount: ptr(int64(1))}.ApplyInScope(ts[1], ScopeThis, others, TransactionRefs{}, txNow)
	require.NoError(t, err)
	assert.Equal(t, []string{"i2"}, []string{changed[0].ID}, "this one only")
	require.Len(t, changed, 1)
}

func TestApplyInScopeKeepsAPurchaseOnOneAccount(t *testing.T) {
	in := cardPurchase("2026-11-24", "")
	in.Installments = 3
	ts := group(t, in, TransactionRefs{Account: txCard, Category: txFood})
	toBank := TransactionPatch{AccountID: ptr("bank")}
	for _, scope := range []EditScope{ScopeThis, ScopeFuture} {
		_, _, err := toBank.ApplyInScope(ts[1], scope, ts[2:], TransactionRefs{Account: txBank}, txNow)
		assert.Equal(t, map[string]string{"scope": CodeInvalid}, fieldsOf(t, err), scope)
	}

	_, changed, err := TransactionPatch{AccountID: ptr("card")}.ApplyInScope(ts[1], ScopeFuture, ts[2:], TransactionRefs{}, txNow)
	require.NoError(t, err)
	assert.Empty(t, changed, "the account it is on")

	_, changed, err = toBank.ApplyInScope(ts[1], ScopeAll, []Transaction{ts[0], ts[2]}, TransactionRefs{Account: txBank}, txNow)
	require.NoError(t, err)
	require.Len(t, changed, 3)
	for _, c := range changed {
		assert.Equal(t, "bank", c.AccountID)
		assert.True(t, c.InvoiceMonth.IsZero(), "off the card")
	}

	single, err := NewTransaction("u1", cardPurchase("2026-11-24", ""), TransactionRefs{Account: txCard, Category: txFood}, txNow)
	require.NoError(t, err)
	next, changed, err := toBank.ApplyInScope(single, ScopeThis, nil, TransactionRefs{Account: txBank}, txNow)
	require.NoError(t, err, "a transaction outside a purchase moves alone")
	assert.Equal(t, "bank", next.AccountID)
	assert.Len(t, changed, 1)
}

func TestApplyInScopeValidation(t *testing.T) {
	ts := group(t, purchaseIn(12, "2100-01-10", 1200), expenseRefs)
	_, _, err := TransactionPatch{Date: ptr("2100-02-10")}.ApplyInScope(ts[0], ScopeAll, ts[1:], TransactionRefs{}, txNow)
	assert.Equal(t, map[string]string{"date": CodeOutOfRange}, fieldsOf(t, err), "the last one would fall in 2101")

	_, _, err = TransactionPatch{Amount: ptr(int64(0))}.ApplyInScope(ts[0], ScopeAll, ts[1:], TransactionRefs{}, txNow)
	assert.Equal(t, map[string]string{"amount": CodeOutOfRange}, fieldsOf(t, err), "the addressed one is checked as always")
}
