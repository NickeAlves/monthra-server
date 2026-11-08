package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// txCard closes on the 25th and falls due on the 5th; txCard2 closes on the
// 3rd and falls due on the 10th.
var (
	txCard  = &Account{ID: "card", Type: AccountCreditCard, Currency: "BRL", Card: card(25, 5, nil)}
	txCard2 = &Account{ID: "card2", Type: AccountCreditCard, Currency: "BRL", Card: card(3, 10, nil)}
)

func cardPurchase(date, invoice string) TransactionInput {
	return TransactionInput{
		Kind: "expense", AccountID: "card", CategoryID: "food", Amount: 4590, Date: date, InvoiceMonth: invoice,
	}
}

func TestNewTransactionPicksTheInvoice(t *testing.T) {
	cardRefs := TransactionRefs{Account: txCard, Category: txFood}
	for date, want := range map[string]string{"2026-11-24": "2026-12", "2026-11-25": "2027-01"} {
		tx, err := NewTransaction("u1", cardPurchase(date, ""), cardRefs, txNow)
		require.NoError(t, err)
		assert.Equal(t, want, tx.InvoiceMonth.String(), "purchase on %s", date)
		assert.True(t, tx.ToInvoiceMonth.IsZero())
	}

	refund, err := NewTransaction("u1", TransactionInput{
		Kind: "income", AccountID: "card", CategoryID: "salary", Amount: 100, Date: "2026-11-08",
	}, TransactionRefs{Account: txCard, Category: txSalary}, txNow)
	require.NoError(t, err)
	assert.Equal(t, "2026-12", refund.InvoiceMonth.String(), "a credit belongs to an invoice too")

	payment, err := NewTransaction("u1", TransactionInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "card", Amount: 50000, Date: "2026-12-05",
	}, TransactionRefs{Account: txBank, ToAccount: txCard}, txNow)
	require.NoError(t, err)
	assert.True(t, payment.InvoiceMonth.IsZero(), "the bank account has no invoice")
	assert.Equal(t, "2026-12", payment.ToInvoiceMonth.String(), "pays the last invoice closed")

	withdrawal, err := NewTransaction("u1", TransactionInput{
		Kind: "transfer", AccountID: "card", ToAccountID: "card2", Amount: 50000, Date: "2026-11-03",
	}, TransactionRefs{Account: txCard, ToAccount: txCard2}, txNow)
	require.NoError(t, err)
	assert.Equal(t, "2026-12", withdrawal.InvoiceMonth.String(), "a transfer out of a card is a charge")
	assert.Equal(t, "2026-11", withdrawal.ToInvoiceMonth.String(), "and the other card's payment")

	plain, err := NewTransaction("u1", expenseInput(), expenseRefs, txNow)
	require.NoError(t, err)
	assert.True(t, plain.InvoiceMonth.IsZero())
}

func TestNewTransactionNamedInvoice(t *testing.T) {
	cardRefs := TransactionRefs{Account: txCard, Category: txFood}
	for _, named := range []string{"2027-01", "2027-12", "2025-12"} {
		tx, err := NewTransaction("u1", cardPurchase("2026-11-24", named), cardRefs, txNow)
		require.NoError(t, err, named)
		assert.Equal(t, named, tx.InvoiceMonth.String())
	}

	payment, err := NewTransaction("u1", TransactionInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "card", Amount: 50000, Date: "2026-11-20",
		ToInvoiceMonth: "2026-12",
	}, TransactionRefs{Account: txBank, ToAccount: txCard}, txNow)
	require.NoError(t, err)
	assert.Equal(t, "2026-12", payment.ToInvoiceMonth.String(), "paid ahead, before it closes")

	for name, tc := range map[string]struct {
		in   TransactionInput
		refs TransactionRefs
		want map[string]string
	}{
		"13 months later":  {cardPurchase("2026-11-24", "2028-01"), cardRefs, map[string]string{"invoiceMonth": CodeOutOfRange}},
		"13 months before": {cardPurchase("2026-11-24", "2025-11"), cardRefs, map[string]string{"invoiceMonth": CodeOutOfRange}},
		"badly written":    {cardPurchase("2026-11-24", "2026-1"), cardRefs, map[string]string{"invoiceMonth": CodeInvalid}},
		"off a card": {
			func() TransactionInput { in := expenseInput(); in.InvoiceMonth = "2026-12"; return in }(),
			expenseRefs, map[string]string{"invoiceMonth": CodeInvalid},
		},
		"a payment to an expense": {
			func() TransactionInput {
				in := cardPurchase("2026-11-24", "")
				in.ToInvoiceMonth = "2026-12"
				return in
			}(),
			cardRefs, map[string]string{"toInvoiceMonth": CodeInvalid},
		},
		"a payment to an account that is no card": {
			TransactionInput{
				Kind: "transfer", AccountID: "card", ToAccountID: "bank", Amount: 1, Date: "2026-11-24", ToInvoiceMonth: "2026-12",
			},
			TransactionRefs{Account: txCard, ToAccount: txBank}, map[string]string{"toInvoiceMonth": CodeInvalid},
		},
	} {
		_, err := NewTransaction("u1", tc.in, tc.refs, txNow)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}

	// Without a valid day there is no invoice to compare with: only the
	// day is reported.
	_, err = NewTransaction("u1", cardPurchase("2026-02-30", "2099-01"), cardRefs, txNow)
	assert.Equal(t, map[string]string{"date": CodeInvalid}, fieldsOf(t, err))
}

func TestTransactionPatchInvoice(t *testing.T) {
	later := txNow.Add(1)
	current, err := NewTransaction("u1", cardPurchase("2026-11-24", ""), TransactionRefs{Account: txCard, Category: txFood}, txNow)
	require.NoError(t, err)
	require.Equal(t, "2026-12", current.InvoiceMonth.String())
	withCard := TransactionRefs{Account: txCard}

	next, changed, err := TransactionPatch{Date: ptr("2026-11-26")}.Apply(current, withCard, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "2027-01", next.InvoiceMonth.String(), "a new day picks the invoice again")

	next, changed, err = TransactionPatch{Date: ptr("2026-11-26")}.Apply(current, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "2026-12", next.InvoiceMonth.String(), "without the card's settings the invoice stays")

	next, _, err = TransactionPatch{Description: ptr("lunch")}.Apply(current, TransactionRefs{}, later)
	require.NoError(t, err)
	assert.Equal(t, "2026-12", next.InvoiceMonth.String(), "other fields leave the invoice alone")

	next, changed, err = TransactionPatch{InvoiceMonth: ptr("2027-01")}.Apply(current, withCard, later)
	require.NoError(t, err)
	assert.True(t, changed, "naming the invoice alone moves the transaction to it")
	assert.Equal(t, "2027-01", next.InvoiceMonth.String())
	assert.Equal(t, current.Date, next.Date)

	moved := next
	next, changed, err = TransactionPatch{InvoiceMonth: ptr("")}.Apply(moved, withCard, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "2026-12", next.InvoiceMonth.String(), "an empty invoice picks it from the day again")

	_, changed, err = TransactionPatch{InvoiceMonth: ptr("2026-12")}.Apply(current, withCard, later)
	require.NoError(t, err)
	assert.False(t, changed, "the same invoice changes nothing")

	next, changed, err = TransactionPatch{AccountID: ptr("bank")}.Apply(current, TransactionRefs{Account: txBank}, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.True(t, next.InvoiceMonth.IsZero(), "off the card, no invoice")

	plain, err := NewTransaction("u1", expenseInput(), expenseRefs, txNow)
	require.NoError(t, err)
	next, _, err = TransactionPatch{AccountID: ptr("card2")}.Apply(plain, TransactionRefs{Account: txCard2}, later)
	require.NoError(t, err)
	assert.Equal(t, "2026-12", next.InvoiceMonth.String(), "onto a card, the invoice of the day")

	for name, tc := range map[string]struct {
		patch TransactionPatch
		refs  TransactionRefs
		want  map[string]string
	}{
		"too far":           {TransactionPatch{InvoiceMonth: ptr("2028-06")}, withCard, map[string]string{"invoiceMonth": CodeOutOfRange}},
		"off a card":        {TransactionPatch{AccountID: ptr("bank"), InvoiceMonth: ptr("2026-12")}, TransactionRefs{Account: txBank}, map[string]string{"invoiceMonth": CodeInvalid}},
		"a payment invoice": {TransactionPatch{ToInvoiceMonth: ptr("")}, withCard, map[string]string{"toInvoiceMonth": CodeInvalid}},
	} {
		_, _, err := tc.patch.Apply(current, tc.refs, later)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}
}

func TestTransferPatchInvoice(t *testing.T) {
	later := txNow.Add(1)
	payment, err := NewTransaction("u1", TransactionInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "card", Amount: 50000, Date: "2026-12-05",
	}, TransactionRefs{Account: txBank, ToAccount: txCard}, txNow)
	require.NoError(t, err)

	next, _, err := TransactionPatch{Date: ptr("2026-12-26")}.Apply(payment, TransactionRefs{ToAccount: txCard}, later)
	require.NoError(t, err)
	assert.Equal(t, "2027-01", next.ToInvoiceMonth.String(), "a later payment pays the invoice closed since")
	assert.True(t, next.InvoiceMonth.IsZero())

	next, _, err = TransactionPatch{ToInvoiceMonth: ptr("2027-01")}.Apply(payment, TransactionRefs{ToAccount: txCard}, later)
	require.NoError(t, err)
	assert.Equal(t, "2027-01", next.ToInvoiceMonth.String())

	next, _, err = TransactionPatch{ToAccountID: ptr("wallet")}.Apply(payment, TransactionRefs{ToAccount: txWallet}, later)
	require.NoError(t, err)
	assert.True(t, next.ToInvoiceMonth.IsZero(), "to an account that is no card, nothing to pay")

	_, _, err = TransactionPatch{InvoiceMonth: ptr("2026-12")}.Apply(payment, TransactionRefs{Account: txBank}, later)
	assert.Equal(t, map[string]string{"invoiceMonth": CodeInvalid}, fieldsOf(t, err), "the bank leg has no invoice")
}
