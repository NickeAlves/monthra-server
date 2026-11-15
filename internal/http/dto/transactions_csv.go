package dto

import (
	"bufio"
	"encoding/csv"
	"io"
	"iter"
	"strconv"
	"strings"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// csvBOM opens the file so spreadsheets read it as UTF-8 (Excel shows the
// accents of a file without it as mojibake).
const csvBOM = "\ufeff"

// csvLayout is how a spreadsheet of one locale reads a CSV file: pt-BR
// and es-ES split columns on ";" because "," is their decimal mark.
type csvLayout struct {
	delimiter rune
	decimal   byte
	// date and month are time layouts.
	date, month string
	header      []string
	kinds       map[domain.TransactionKind]string
}

var csvLayouts = map[i18n.Locale]csvLayout{
	i18n.PtBR: {
		delimiter: ';', decimal: ',', date: "02/01/2006", month: "01/2006",
		header: []string{
			"Data", "Tipo", "Descrição", "Categoria", "Conta", "Valor", "Moeda",
			"Conta de destino", "Valor de destino", "Moeda de destino", "Tags", "Parcela", "Fatura do cartão",
		},
		kinds: map[domain.TransactionKind]string{
			domain.TransactionExpense: "Despesa", domain.TransactionIncome: "Receita", domain.TransactionTransfer: "Transferência",
		},
	},
	i18n.EsES: {
		delimiter: ';', decimal: ',', date: "02/01/2006", month: "01/2006",
		header: []string{
			"Fecha", "Tipo", "Descripción", "Categoría", "Cuenta", "Importe", "Moneda",
			"Cuenta de destino", "Importe de destino", "Moneda de destino", "Etiquetas", "Cuota", "Factura de la tarjeta",
		},
		kinds: map[domain.TransactionKind]string{
			domain.TransactionExpense: "Gasto", domain.TransactionIncome: "Ingreso", domain.TransactionTransfer: "Transferencia",
		},
	},
	i18n.EnUS: {
		delimiter: ',', decimal: '.', date: "2006-01-02", month: "2006-01",
		header: []string{
			"Date", "Type", "Description", "Category", "Account", "Amount", "Currency",
			"Destination account", "Destination amount", "Destination currency", "Tags", "Installment", "Card invoice",
		},
		kinds: map[domain.TransactionKind]string{
			domain.TransactionExpense: "Expense", domain.TransactionIncome: "Income", domain.TransactionTransfer: "Transfer",
		},
	},
}

// TransactionsCSV writes transactions as CSV in the layout of Locale, with
// the names of the accounts and categories they reference. The amount is
// signed so a column sum is what the owner's accounts gained: an expense
// and a transfer out are negative, income positive; a transfer's
// destination amount is what arrived.
type TransactionsCSV struct {
	Locale     i18n.Locale
	Accounts   map[string]domain.Account
	Categories map[string]domain.Category
}

// Write writes the byte order mark, the header and a row per transaction
// of rows to w, stopping at the first error of rows or of w. It writes
// through a buffer, so nothing reaches w before the first rows were read:
// a first read that fails leaves w untouched.
func (c TransactionsCSV) Write(w io.Writer, rows iter.Seq2[domain.Transaction, error]) error {
	layout, ok := csvLayouts[c.Locale]
	if !ok {
		layout = csvLayouts[i18n.Default]
	}
	buf := bufio.NewWriter(w)
	if _, err := buf.WriteString(csvBOM); err != nil {
		return err
	}
	// csv.NewWriter reuses buf (a bufio.Writer of the same size).
	out := csv.NewWriter(buf)
	out.Comma = layout.delimiter
	// RFC 4180 and Excel end records with CRLF.
	out.UseCRLF = true
	if err := out.Write(layout.header); err != nil {
		return err
	}
	for t, err := range rows {
		if err != nil {
			return err
		}
		if err := out.Write(c.row(layout, t)); err != nil {
			return err
		}
	}
	out.Flush()
	if err := out.Error(); err != nil {
		return err
	}
	return buf.Flush()
}

// row is the record of t.
func (c TransactionsCSV) row(l csvLayout, t domain.Transaction) []string {
	amount := t.Amount
	if t.Kind != domain.TransactionIncome {
		amount = -amount
	}
	var category string
	if cat, ok := c.Categories[t.CategoryID]; ok && t.CategoryID != "" {
		category = cat.DisplayName(c.Locale)
	}
	rec := []string{
		t.Date.Time().Format(l.date),
		l.kinds[t.Kind],
		csvText(t.Description),
		csvText(category),
		csvText(c.Accounts[t.AccountID].Name),
		formatMinor(amount, l.decimal),
		t.Currency,
		"", "", "",
		csvText(strings.Join(t.Tags, ", ")),
		"",
		"",
	}
	if t.IsTransfer() {
		rec[7] = csvText(c.Accounts[t.ToAccountID].Name)
		rec[8] = formatMinor(t.ToAmount, l.decimal)
		rec[9] = t.ToCurrency
	}
	if t.Installment != nil {
		rec[11] = strconv.Itoa(t.Installment.Number) + "/" + strconv.Itoa(t.Installment.Count)
	}
	// The invoice of the card the transaction is on, or the one a transfer
	// pays.
	invoice := t.InvoiceMonth
	if invoice.IsZero() {
		invoice = t.ToInvoiceMonth
	}
	if !invoice.IsZero() {
		rec[12] = invoice.Day(1).Time().Format(l.month)
	}
	return rec
}

// csvText keeps text a spreadsheet would read as a formula ("=HYPERLINK(…)",
// "+1", "-1+1", "@SUM(…)", a leading tab or carriage return) as text, by
// prefixing it with an apostrophe (OWASP CSV injection). Numbers are written
// by formatMinor and never pass here.
func csvText(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// formatMinor writes v minor units (cents) with two decimals after mark and
// no thousands separator, so spreadsheets read it as a number.
func formatMinor(v int64, mark byte) string {
	var b strings.Builder
	// Amounts stay within domain.MaxAmount, so -v never overflows.
	if v < 0 {
		b.WriteByte('-')
		v = -v
	}
	b.WriteString(strconv.FormatInt(v/100, 10))
	b.WriteByte(mark)
	if v%100 < 10 {
		b.WriteByte('0')
	}
	b.WriteString(strconv.FormatInt(v%100, 10))
	return b.String()
}
