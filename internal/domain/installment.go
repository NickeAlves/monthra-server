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

// ApplyInScope applies the patch to current (as Apply does) and, when
// current is an installment and scope reaches further, to others: the
// other installments of its purchase within scope, as the caller found
// them. It returns current after the patch and every transaction that
// changed, current first when it did.
//
// The other installments take the changes the patch made to current, so a
// client that sends its whole form changes nothing it left as it was:
//   - the amount (of each installment), category, description, tags and
//     account, with their values on current;
//   - the day: installment j falls on current's new day j-k months away (k
//     being current's number), the last day of a shorter month;
//   - the invoice, when the day, the account or the invoice of current
//     changed: current's invoice plus j-k, or none off a card.
//
// A purchase stays on one account: moving an installment to another
// account takes ScopeAll, and answers scope: invalid otherwise.
func (p TransactionPatch) ApplyInScope(current Transaction, scope EditScope, others []Transaction, refs TransactionRefs, now time.Time) (Transaction, []Transaction, error) {
	inGroup := current.Installment != nil
	if inGroup && scope != ScopeAll && movesAccount(p.AccountID, current.AccountID) {
		return Transaction{}, nil, &ValidationError{Fields: []FieldError{{Field: "scope", Code: CodeInvalid}}}
	}
	next, changed, err := p.Apply(current, refs, now)
	if err != nil {
		return Transaction{}, nil, err
	}
	var out []Transaction
	if changed {
		out = append(out, next)
	}
	if !inGroup || scope == ScopeThis {
		return next, out, nil
	}

	redated := !next.Date.Equal(current.Date)
	accountMoved := next.AccountID != current.AccountID
	reinvoiced := redated || accountMoved || next.InvoiceMonth != current.InvoiceMonth
	var v Validation
	for _, o := range others {
		if o.ID == current.ID || o.Installment == nil || o.Installment.GroupID != current.Installment.GroupID {
			continue
		}
		shift := o.Installment.Number - current.Installment.Number
		n := o
		n.Tags = slices.Clone(o.Tags)
		if next.Amount != current.Amount {
			n.Amount = next.Amount
		}
		if next.CategoryID != current.CategoryID {
			n.CategoryID = next.CategoryID
		}
		if next.Description != current.Description {
			n.Description = next.Description
		}
		if !slices.Equal(next.Tags, current.Tags) {
			n.Tags = slices.Clone(next.Tags)
		}
		if accountMoved {
			n.AccountID, n.Currency = next.AccountID, next.Currency
		}
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
		if !n.equal(o) {
			n.UpdatedAt = now.UTC()
			out = append(out, n)
		}
	}
	if err := v.Err(); err != nil {
		return Transaction{}, nil, err
	}
	return next, out, nil
}

// movesAccount reports whether a patch names an account other than the
// current one; an empty one is the patch's own error (required).
func movesAccount(patch *string, current string) bool {
	return patch != nil && *patch != "" && *patch != current
}
