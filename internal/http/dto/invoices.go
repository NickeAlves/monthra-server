package dto

import (
	"net/url"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Invoice is the public view of a card invoice. Amounts are integers in
// minor units (cents) of the card's currency; what an invoice leaves unpaid
// rolls into the next one as its previousBalance, and a negative balance is
// a credit.
type Invoice struct {
	// Month is the month the invoice falls due in, "YYYY-MM".
	Month       string `json:"month"`
	ClosingDate string `json:"closingDate"`
	DueDate     string `json:"dueDate"`
	// Status is upcoming, open, closed, paid, overdue or carried_over.
	Status          string `json:"status"`
	Currency        string `json:"currency"`
	PreviousBalance int64  `json:"previousBalance"`
	Charges         int64  `json:"charges"`
	Credits         int64  `json:"credits"`
	Total           int64  `json:"total"`
	Paid            int64  `json:"paid"`
	Remaining       int64  `json:"remaining"`
}

// InvoicesResponse is the body of GET /accounts/{id}/invoices: one invoice
// per month, oldest first.
type InvoicesResponse struct {
	Invoices []Invoice `json:"invoices"`
}

// NewInvoices maps the invoices; none is [], never null.
func NewInvoices(list []domain.Invoice) InvoicesResponse {
	out := InvoicesResponse{Invoices: make([]Invoice, len(list))}
	for i, inv := range list {
		out.Invoices[i] = Invoice{
			Month: inv.Month.String(), ClosingDate: inv.ClosingDate.String(), DueDate: inv.DueDate.String(),
			Status: string(inv.Status), Currency: inv.Currency, PreviousBalance: inv.PreviousBalance,
			Charges: inv.Charges, Credits: inv.Credits, Total: inv.Total, Paid: inv.Paid, Remaining: inv.Remaining,
		}
	}
	return out
}

// InvoiceQuery is a parsed GET /accounts/{id}/invoices query; zero fields
// were not given.
type InvoiceQuery struct {
	From, To domain.Month
	AsOf     domain.Date
}

// ParseInvoiceQuery reads ?from=YYYY-MM&to=YYYY-MM&asOf=YYYY-MM-DD, or
// returns a *domain.ValidationError naming each invalid parameter.
func ParseInvoiceQuery(q url.Values) (InvoiceQuery, error) {
	var v domain.Validation
	var out InvoiceQuery
	var err error
	if raw := q.Get("from"); raw != "" {
		out.From, err = domain.ParseMonth(raw)
		v.Check("from", err)
	}
	if raw := q.Get("to"); raw != "" {
		out.To, err = domain.ParseMonth(raw)
		v.Check("to", err)
	}
	if !out.From.IsZero() && !out.To.IsZero() && out.To.Before(out.From) {
		v.Add("to", domain.CodeInvalid)
	}
	if raw := q.Get("asOf"); raw != "" {
		out.AsOf, err = domain.ParseDate(raw)
		v.Check("asOf", err)
	}
	if err := v.Err(); err != nil {
		return InvoiceQuery{}, err
	}
	return out, nil
}
