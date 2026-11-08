package domain

// Bounds of the card invoices.
const (
	// MaxInvoiceShift is how many months the invoice the owner names for a
	// transaction may lie from the one its day falls in: room for a purchase
	// the bank posted late or a payment made early, a guard against typos.
	MaxInvoiceShift = 12
	// MaxInvoiceRange is how many invoices one read returns.
	MaxInvoiceRange = 24
)

// The invoice cycle of a credit card. An invoice is named after the month it
// falls due in, as banks name them: the December invoice is due on DueDay of
// December. It closes on ClosingDay of that month when DueDay comes later,
// or of the month before otherwise. A day past the end of a shorter month is
// its last day.

// ClosingDate returns the day invoice m closes: the purchases of the days
// before it belong to m, those of that day on to the next invoice.
func (c *CardSettings) ClosingDate(m Month) Date {
	if c.DueDay > c.ClosingDay {
		return m.Day(c.ClosingDay)
	}
	return m.AddMonths(-1).Day(c.ClosingDay)
}

// DueDate returns the day invoice m falls due.
func (c *CardSettings) DueDate(m Month) Date { return m.Day(c.DueDay) }

// InvoiceFor returns the invoice of a purchase on day d: the first one that
// closes after d, so a purchase on the closing day goes to the next invoice
// (the "best day to buy").
func (c *CardSettings) InvoiceFor(d Date) Month {
	// The invoice that closes within the month of d.
	m := MonthOf(d)
	if c.DueDay <= c.ClosingDay {
		m = m.AddMonths(1)
	}
	if d.Before(c.ClosingDate(m)) {
		return m
	}
	return m.AddMonths(1)
}

// PaymentInvoice returns the invoice a payment on day d pays when the owner
// names none: the last one closed on or before d.
func (c *CardSettings) PaymentInvoice(d Date) Month { return c.InvoiceFor(d).AddMonths(-1) }

// IsCard reports whether a is a credit card account, with its settings.
func (a Account) IsCard() bool { return a.Type == AccountCreditCard && a.Card != nil }

// InvoiceStatus is where an invoice stands on a given day.
type InvoiceStatus string

// Invoice statuses.
const (
	// InvoiceUpcoming has not started: the invoice before it is still open.
	InvoiceUpcoming InvoiceStatus = "upcoming"
	// InvoiceOpen takes the purchases of the current cycle.
	InvoiceOpen InvoiceStatus = "open"
	// InvoiceClosed closed and waits for its payment, not yet due.
	InvoiceClosed InvoiceStatus = "closed"
	// InvoicePaid closed and nothing of it remains to pay.
	InvoicePaid InvoiceStatus = "paid"
	// InvoiceOverdue is past its due day with something left to pay.
	InvoiceOverdue InvoiceStatus = "overdue"
	// InvoiceCarriedOver closed with something left to pay, which the next
	// invoice, closed since, took as its previous balance.
	InvoiceCarriedOver InvoiceStatus = "carried_over"
)

// InvoiceSums are the transactions of one invoice of a card, in minor units
// of its currency: Charges are expenses and transfers out of the card,
// Credits income on it (refunds, cashback), Payments what transfers to it
// brought.
type InvoiceSums struct {
	Month    Month
	Charges  int64
	Credits  int64
	Payments int64
}

// net is what the invoice adds to the debt of the card.
func (s InvoiceSums) net() int64 { return s.Charges - s.Credits - s.Payments }

// Invoice is the statement of a card for one month, in minor units of its
// currency. What an invoice leaves unpaid rolls into the next one as its
// PreviousBalance, with no interest; a negative balance is a credit.
type Invoice struct {
	Month       Month
	ClosingDate Date
	DueDate     Date
	Status      InvoiceStatus
	Currency    string
	// PreviousBalance is what the invoices before left unpaid, the card's
	// opening debt (a negative initial balance) included.
	PreviousBalance int64
	Charges         int64
	Credits         int64
	// Total is PreviousBalance + Charges - Credits: what the invoice asks.
	Total int64
	// Paid is what the payments to this invoice brought.
	Paid int64
	// Remaining is Total - Paid; negative when the owner paid more.
	Remaining int64
}

// BuildInvoices returns the invoices of card a from month from to month to,
// one per month (those without transactions included), as they stand on day
// asOf. sums holds the invoices of a up to to at least, in any order; the
// ones before from make the first previous balance. The closing and due
// days come from the card's current settings. It returns nil when a is not
// a card.
func BuildInvoices(a Account, sums []InvoiceSums, from, to Month, asOf Date) []Invoice {
	if !a.IsCard() || to.Before(from) {
		return nil
	}
	carry := -a.InitialBalance
	inRange := make(map[Month]InvoiceSums, to.Sub(from)+1)
	for _, s := range sums {
		switch {
		case s.Month.Before(from):
			carry += s.net()
		case to.Before(s.Month):
			// After the range: it changes none of the invoices asked for.
		default:
			acc := inRange[s.Month]
			acc.Charges += s.Charges
			acc.Credits += s.Credits
			acc.Payments += s.Payments
			inRange[s.Month] = acc
		}
	}

	out := make([]Invoice, 0, to.Sub(from)+1)
	for m := from; !to.Before(m); m = m.AddMonths(1) {
		s := inRange[m]
		inv := Invoice{
			Month: m, ClosingDate: a.Card.ClosingDate(m), DueDate: a.Card.DueDate(m), Currency: a.Currency,
			PreviousBalance: carry, Charges: s.Charges, Credits: s.Credits, Paid: s.Payments,
		}
		inv.Total = carry + s.Charges - s.Credits
		inv.Remaining = inv.Total - inv.Paid
		inv.Status = a.Card.invoiceStatus(m, inv.Remaining, asOf)
		out = append(out, inv)
		carry = inv.Remaining
	}
	return out
}

// invoiceStatus is where invoice m, with remaining left to pay, stands on
// day asOf.
func (c *CardSettings) invoiceStatus(m Month, remaining int64, asOf Date) InvoiceStatus {
	switch {
	case asOf.Before(c.ClosingDate(m.AddMonths(-1))):
		return InvoiceUpcoming
	case asOf.Before(c.ClosingDate(m)):
		return InvoiceOpen
	case remaining <= 0:
		return InvoicePaid
	case !asOf.Before(c.ClosingDate(m.AddMonths(1))):
		return InvoiceCarriedOver
	case c.DueDate(m).Before(asOf):
		return InvoiceOverdue
	default:
		return InvoiceClosed
	}
}
