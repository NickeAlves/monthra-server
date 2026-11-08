package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCardInvoiceCycle(t *testing.T) {
	for name, tc := range map[string]struct {
		card    *CardSettings
		invoice string
		closing string
		due     string
		// purchases maps a day to the invoice it belongs to.
		purchases map[string]string
		// payments maps a day to the invoice a payment on it pays.
		payments map[string]string
	}{
		"due the month after closing": {
			card: card(25, 5, nil), invoice: "2026-12", closing: "2026-11-25", due: "2026-12-05",
			purchases: map[string]string{
				"2026-10-25": "2026-12",
				"2026-11-01": "2026-12",
				"2026-11-24": "2026-12",
				"2026-11-25": "2027-01", // the closing day goes to the next invoice
				"2026-12-31": "2027-02",
			},
			payments: map[string]string{
				"2026-11-24": "2026-11",
				"2026-11-25": "2026-12", // the invoice that closed that day
				"2026-12-05": "2026-12",
				"2026-12-24": "2026-12",
			},
		},
		"due the month it closes": {
			card: card(3, 10, nil), invoice: "2026-11", closing: "2026-11-03", due: "2026-11-10",
			purchases: map[string]string{
				"2026-11-02": "2026-11",
				"2026-11-03": "2026-12",
				"2026-12-31": "2027-01",
			},
			payments: map[string]string{"2026-11-10": "2026-11", "2026-11-02": "2026-10"},
		},
		"closing on the 31st of February": {
			card: card(31, 10, nil), invoice: "2027-03", closing: "2027-02-28", due: "2027-03-10",
			purchases: map[string]string{
				"2027-02-27": "2027-03",
				"2027-02-28": "2027-04",
				"2028-02-28": "2028-03", // leap year: closes on the 29th
				"2028-02-29": "2028-04",
			},
		},
		"closing and due on the same day": {
			card: card(10, 10, nil), invoice: "2026-12", closing: "2026-11-10", due: "2026-12-10",
			purchases: map[string]string{"2026-11-09": "2026-12", "2026-11-10": "2027-01"},
		},
		"due on the 31st of a short month": {
			card: card(20, 31, nil), invoice: "2026-11", closing: "2026-11-20", due: "2026-11-30",
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := month(t, tc.invoice)
			assert.Equal(t, tc.closing, tc.card.ClosingDate(m).String())
			assert.Equal(t, tc.due, tc.card.DueDate(m).String())
			for d, want := range tc.purchases {
				assert.Equal(t, want, tc.card.InvoiceFor(day(t, d)).String(), "purchase on %s", d)
			}
			for d, want := range tc.payments {
				assert.Equal(t, want, tc.card.PaymentInvoice(day(t, d)).String(), "payment on %s", d)
			}
		})
	}
}

func TestAccountIsCard(t *testing.T) {
	assert.True(t, Account{Type: AccountCreditCard, Card: card(25, 5, nil)}.IsCard())
	assert.False(t, Account{Type: AccountCreditCard}.IsCard(), "a card without settings has no invoices")
	assert.False(t, Account{Type: AccountChecking}.IsCard())
}

// invoiceCard closes on the 25th and falls due on the 5th, with an opening
// debt of 100.00.
var invoiceCard = Account{ID: "card", Type: AccountCreditCard, Currency: "BRL", InitialBalance: -10000, Card: card(25, 5, nil)}

func TestBuildInvoicesCarriesWhatIsLeft(t *testing.T) {
	sums := []InvoiceSums{
		{Month: month(t, "2027-03"), Charges: 999}, // after the range
		{Month: month(t, "2026-12"), Charges: 30000},
		{Month: month(t, "2026-11"), Charges: 20000, Credits: 1000, Payments: 14000},
		{Month: month(t, "2026-10"), Charges: 5000}, // before the range
	}
	got := BuildInvoices(invoiceCard, sums, month(t, "2026-11"), month(t, "2027-01"), day(t, "2026-11-30"))
	assert.Equal(t, []Invoice{
		{
			Month: month(t, "2026-11"), ClosingDate: day(t, "2026-10-25"), DueDate: day(t, "2026-11-05"),
			Status: InvoiceCarriedOver, Currency: "BRL",
			// The opening debt and October's charges.
			PreviousBalance: 15000, Charges: 20000, Credits: 1000, Total: 34000, Paid: 14000, Remaining: 20000,
		},
		{
			Month: month(t, "2026-12"), ClosingDate: day(t, "2026-11-25"), DueDate: day(t, "2026-12-05"),
			Status: InvoiceClosed, Currency: "BRL",
			PreviousBalance: 20000, Charges: 30000, Total: 50000, Remaining: 50000,
		},
		{
			Month: month(t, "2027-01"), ClosingDate: day(t, "2026-12-25"), DueDate: day(t, "2027-01-05"),
			Status: InvoiceOpen, Currency: "BRL",
			PreviousBalance: 50000, Total: 50000, Remaining: 50000,
		},
	}, got, "one invoice per month, the empty one included")
}

func TestBuildInvoicesCredit(t *testing.T) {
	a := invoiceCard
	a.InitialBalance = 0
	sums := []InvoiceSums{
		{Month: month(t, "2026-12"), Charges: 10000, Payments: 15000}, // paid 50.00 too much
		{Month: month(t, "2027-01"), Charges: 2000},
	}
	got := BuildInvoices(a, sums, month(t, "2026-12"), month(t, "2027-01"), day(t, "2027-01-10"))
	assert.Equal(t, int64(-5000), got[0].Remaining)
	assert.Equal(t, InvoicePaid, got[0].Status)
	assert.Equal(t, int64(-5000), got[1].PreviousBalance, "a credit rolls over too")
	assert.Equal(t, int64(-3000), got[1].Total)
	assert.Equal(t, InvoicePaid, got[1].Status, "a closed invoice the credit covers is paid")
}

func TestBuildInvoicesBounds(t *testing.T) {
	nov := month(t, "2026-11")
	assert.Nil(t, BuildInvoices(Account{Type: AccountChecking}, nil, nov, nov, day(t, "2026-11-08")))
	assert.Nil(t, BuildInvoices(invoiceCard, nil, nov, nov.AddMonths(-1), day(t, "2026-11-08")))
	got := BuildInvoices(invoiceCard, nil, nov, nov, day(t, "2026-11-08"))
	assert.Len(t, got, 1)
	assert.Equal(t, int64(10000), got[0].Remaining, "the opening debt alone")
}

func TestInvoiceStatus(t *testing.T) {
	// The December invoice of invoiceCard: its cycle runs from October 25
	// (when November's closed) to November 25, it falls due on December 5,
	// and January's closes on December 25.
	dec := month(t, "2026-12")
	for _, tc := range []struct {
		asOf      string
		remaining int64
		want      InvoiceStatus
	}{
		{"2026-10-24", 100, InvoiceUpcoming},
		{"2026-10-25", 100, InvoiceOpen},
		{"2026-11-24", 100, InvoiceOpen},
		{"2026-11-24", 0, InvoiceOpen},
		{"2026-11-25", 100, InvoiceClosed},
		{"2026-12-05", 100, InvoiceClosed},
		{"2026-12-06", 100, InvoiceOverdue},
		{"2026-12-24", 100, InvoiceOverdue},
		{"2026-12-25", 100, InvoiceCarriedOver},
		{"2026-11-25", 0, InvoicePaid},
		{"2026-12-06", -1, InvoicePaid},
		{"2027-06-01", 0, InvoicePaid},
	} {
		got := invoiceCard.Card.invoiceStatus(dec, tc.remaining, day(t, tc.asOf))
		assert.Equal(t, tc.want, got, "on %s with %d left", tc.asOf, tc.remaining)
	}
}
