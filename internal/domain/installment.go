package domain

import (
	"slices"
	"time"
)

// Bounds of an installment purchase.
const (
	MinInstallments = 2
	MaxInstallments = 48
)

// Installment places a transaction in an installment purchase: one expense
// split into Count monthly transactions, numbered from 1, that share
// GroupID. It never changes once written: deleting some installments leaves
// the others numbered as they were ("3/12").
type Installment struct {
	// GroupID is the same on every installment of a purchase; the store
	// assigns it.
	GroupID string
	Number  int
	Count   int
}

// checkInstallments validates the number of installments n of a purchase
// of kind and amount on day (each already checked on its own; zero values
// were invalid): an expense only, MinInstallments to MaxInstallments, at
// least one minor unit each, the last one on or before MaxDate.
func checkInstallments(v *Validation, kind TransactionKind, n int, amount int64, day Date) {
	switch {
	case kind != "" && kind != TransactionExpense:
		v.Add("installments", CodeInvalid)
	case n < MinInstallments || n > MaxInstallments:
		v.Add("installments", CodeOutOfRange)
	case amount > 0 && amount < int64(n):
		v.Add("installments", CodeOutOfRange)
	case !day.IsZero() && MaxDate.Before(day.AddMonths(n-1)):
		v.Add("installments", CodeOutOfRange)
	}
}

// NewTransactions validates in as NewTransaction does and returns what it
// creates: the transaction, or, when in names Installments, the
// installments of the purchase. Installment i (from 1) takes Amount/n, the
// first one the remainder too; it falls on the day of the purchase i-1
// months later (the last day of a shorter month), and on a card in the
// first one's invoice plus i-1, since a card charges one installment per
// invoice.
func NewTransactions(userID string, in TransactionInput, refs TransactionRefs, now time.Time) ([]Transaction, error) {
	t, err := NewTransaction(userID, in, refs, now)
	if err != nil {
		return nil, err
	}
	if in.Installments == 0 {
		return []Transaction{t}, nil
	}
	n := in.Installments
	each, rest := t.Amount/int64(n), t.Amount%int64(n)
	out := make([]Transaction, n)
	for i := range n {
		inst := t
		inst.Tags = slices.Clone(t.Tags)
		inst.Amount = each
		if i == 0 {
			inst.Amount += rest
		}
		inst.Date = t.Date.AddMonths(i)
		if !t.InvoiceMonth.IsZero() {
			inst.InvoiceMonth = t.InvoiceMonth.AddMonths(i)
		}
		inst.Installment = &Installment{Number: i + 1, Count: n}
		out[i] = inst
	}
	return out, nil
}

// SeriesEdit is what an edit of one transaction of a series changes: the
// addressed transaction after the edit (Next, and whether it changed) and
// the other transactions of its series that changed. Callers store Others
// first and Next last: until Next is stored, sending the same edit again
// takes the same changes from it and completes what a failure left out.
type SeriesEdit struct {
	Next    Transaction
	Changed bool
	Others  []Transaction
}

// ApplyInScope applies the patch to current (as Apply does) and, when
// current belongs to a series (an installment purchase or a recurrence)
// and scope reaches further, to others: the other transactions of its
// series within scope, as the caller found them.
//
// The others take the changes the patch made to current, so a client that
// sends its whole form changes nothing it left as it was: the amount (of
// each one), category, description, tags and account, with their values on
// current.
//
// On the installments of a purchase:
//   - the day: installment j falls on current's new day j-k months away (k
//     being current's number), the last day of a shorter month;
//   - the invoice, when the day, the account or the invoice of current
//     changed: current's invoice plus j-k, or none off a card;
//   - a purchase stays on one account: moving an installment to another
//     account takes ScopeAll, and answers scope: invalid otherwise.
//
// On the occurrences of a recurrence:
//   - the day and the invoice belong to each occurrence (the rule gives the
//     days): changing them beyond ScopeThis answers scope: invalid;
//   - a transfer's destination and the amount it adds follow too;
//   - an occurrence moved to another account picks its invoice again from
//     its own day, on refs' card.
func (p TransactionPatch) ApplyInScope(current Transaction, scope EditScope, others []Transaction, refs TransactionRefs, now time.Time) (SeriesEdit, error) {
	purchase, recurrence := current.Installment != nil, current.Recurrence != nil
	if purchase && scope != ScopeAll && movesAccount(p.AccountID, current.AccountID) {
		return SeriesEdit{}, scopeInvalid()
	}
	next, changed, err := p.Apply(current, refs, now)
	if err != nil {
		return SeriesEdit{}, err
	}
	edit := SeriesEdit{Next: next, Changed: changed}
	switch {
	case scope == ScopeThis:
		return edit, nil
	case purchase:
		return edit, edit.installments(current, others, now)
	case recurrence:
		return edit, edit.occurrences(current, others, refs, now)
	default:
		return edit, nil
	}
}

func scopeInvalid() error {
	return &ValidationError{Fields: []FieldError{{Field: "scope", Code: CodeInvalid}}}
}

// installments adds to e.Others the installments of others, in current's
// purchase, that change with e.Next.
func (e *SeriesEdit) installments(current Transaction, others []Transaction, now time.Time) error {
	next := e.Next
	redated := !next.Date.Equal(current.Date)
	reinvoiced := redated || next.AccountID != current.AccountID || next.InvoiceMonth != current.InvoiceMonth
	var v Validation
	for _, o := range others {
		if o.ID == current.ID || o.Installment == nil || o.Installment.GroupID != current.Installment.GroupID {
			continue
		}
		shift := o.Installment.Number - current.Installment.Number
		n := carryChanges(o, current, next)
		if redated {
			n.Date = next.Date.AddMonths(shift)
			if n.Date.Before(MinDate) || MaxDate.Before(n.Date) {
				v.Add("date", CodeOutOfRange)
				break
			}
		}
		if reinvoiced {
			n.InvoiceMonth = Month{}
			if !next.InvoiceMonth.IsZero() {
				n.InvoiceMonth = next.InvoiceMonth.AddMonths(shift)
			}
		}
		e.add(n, o, now)
	}
	if err := v.Err(); err != nil {
		*e = SeriesEdit{}
		return err
	}
	return nil
}

// occurrences adds to e.Others the occurrences of others, of current's
// recurrence, that change with e.Next.
func (e *SeriesEdit) occurrences(current Transaction, others []Transaction, refs TransactionRefs, now time.Time) error {
	next := e.Next
	moved := next.AccountID != current.AccountID
	toMoved := next.ToAccountID != current.ToAccountID
	if !next.Date.Equal(current.Date) || (!moved && next.InvoiceMonth != current.InvoiceMonth) ||
		(!toMoved && next.ToInvoiceMonth != current.ToInvoiceMonth) {
		*e = SeriesEdit{}
		return scopeInvalid()
	}
	for _, o := range others {
		if o.ID == current.ID || o.Recurrence == nil || o.Recurrence.ID != current.Recurrence.ID {
			continue
		}
		n := carryTransfer(carryChanges(o, current, next), current, next)
		if moved {
			n.InvoiceMonth = invoiceOn(refs.Account, n.AccountID, n.Date, (*CardSettings).InvoiceFor)
		}
		if toMoved {
			n.ToInvoiceMonth = invoiceOn(refs.ToAccount, n.ToAccountID, n.Date, (*CardSettings).PaymentInvoice)
		}
		e.add(n, o, now)
	}
	return nil
}

// add records n, the edit of o, when it changed.
func (e *SeriesEdit) add(n, o Transaction, now time.Time) {
	if !n.equal(o) {
		n.UpdatedAt = now.UTC()
		e.Others = append(e.Others, n)
	}
}

// carryChanges returns o with the changes from before to after (an edit of
// another transaction of its series): the amount, the category, the
// description, the tags and the account with its currency.
func carryChanges(o, before, after Transaction) Transaction {
	n := o
	n.Tags = slices.Clone(o.Tags)
	if after.Amount != before.Amount {
		n.Amount = after.Amount
	}
	if after.CategoryID != before.CategoryID {
		n.CategoryID = after.CategoryID
	}
	if after.Description != before.Description {
		n.Description = after.Description
	}
	if !slices.Equal(after.Tags, before.Tags) {
		n.Tags = slices.Clone(after.Tags)
	}
	if after.AccountID != before.AccountID {
		n.AccountID, n.Currency = after.AccountID, after.Currency
	}
	return n
}

// carryTransfer is carryChanges for the destination of a transfer and the
// amount it adds there.
func carryTransfer(o, before, after Transaction) Transaction {
	if after.ToAccountID != before.ToAccountID {
		o.ToAccountID, o.ToCurrency = after.ToAccountID, after.ToCurrency
	}
	if after.ToAmount != before.ToAmount {
		o.ToAmount = after.ToAmount
	}
	return o
}

// movesAccount reports whether a patch names an account other than the
// current one; an empty one is the patch's own error (required).
func movesAccount(patch *string, current string) bool {
	return patch != nil && *patch != "" && *patch != current
}
