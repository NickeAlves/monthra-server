package dto

import (
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

func mustDate(t *testing.T, s string) domain.Date {
	t.Helper()
	d, err := domain.ParseDate(s)
	require.NoError(t, err)
	return d
}

func mustMonth(t *testing.T, s string) domain.Month {
	t.Helper()
	m, err := domain.ParseMonth(s)
	require.NoError(t, err)
	return m
}

func seqOf(list []domain.Transaction, err error) iter.Seq2[domain.Transaction, error] {
	return func(yield func(domain.Transaction, error) bool) {
		for _, t := range list {
			if !yield(t, nil) {
				return
			}
		}
		if err != nil {
			yield(domain.Transaction{}, err)
		}
	}
}

// csvFixture is a purchase in installments on a card with formula-looking
// text, a salary, a transfer paying the card from a USD account and an
// expense whose account and category are gone.
func csvFixture(t *testing.T, l i18n.Locale) (TransactionsCSV, []domain.Transaction) {
	t.Helper()
	c := TransactionsCSV{
		Locale: l,
		Accounts: map[string]domain.Account{
			"bank": {ID: "bank", Name: "Conta; \"principal\""},
			"card": {ID: "card", Name: "=Cartão"},
			"usd":  {ID: "usd", Name: "Wallet"},
		},
		Categories: map[string]domain.Category{
			"groceries": {ID: "groceries", DefaultKey: "groceries", Kind: domain.CategoryExpense},
			"salary":    {ID: "salary", Name: "Salário CLT", Kind: domain.CategoryIncome},
		},
	}
	return c, []domain.Transaction{
		{
			Kind: domain.TransactionExpense, AccountID: "card", Currency: "BRL", CategoryID: "groceries", Amount: 123456,
			Date: mustDate(t, "2026-11-08"), InvoiceMonth: mustMonth(t, "2026-12"), Description: "-1+1 Café",
			Tags: []string{"@home", "trip"}, Installment: &domain.Installment{GroupID: "g", Number: 3, Count: 10},
		},
		{Kind: domain.TransactionIncome, AccountID: "bank", Currency: "BRL", CategoryID: "salary", Amount: 500005, Date: mustDate(t, "2026-11-05")},
		{
			Kind: domain.TransactionTransfer, AccountID: "usd", Currency: "USD", Amount: 2000, ToAccountID: "card",
			ToCurrency: "BRL", ToAmount: 10007, ToInvoiceMonth: mustMonth(t, "2026-11"), Date: mustDate(t, "2026-11-01"),
		},
		{Kind: domain.TransactionExpense, AccountID: "gone", Currency: "BRL", CategoryID: "gone", Amount: 5, Date: mustDate(t, "2026-10-31")},
	}
}

func TestTransactionsCSVPtBR(t *testing.T) {
	c, txs := csvFixture(t, i18n.PtBR)
	var b strings.Builder
	require.NoError(t, c.Write(&b, seqOf(txs, nil)))
	assert.Equal(t, "\ufeff"+
		"Data;Tipo;Descrição;Categoria;Conta;Valor;Moeda;Conta de destino;Valor de destino;Moeda de destino;Tags;Parcela;Fatura do cartão\r\n"+
		"08/11/2026;Despesa;'-1+1 Café;Mercado;'=Cartão;-1234,56;BRL;;;;'@home, trip;3/10;12/2026\r\n"+
		"05/11/2026;Receita;;Salário CLT;\"Conta; \"\"principal\"\"\";5000,05;BRL;;;;;;\r\n"+
		"01/11/2026;Transferência;;;Wallet;-20,00;USD;'=Cartão;100,07;BRL;;;11/2026\r\n"+
		"31/10/2026;Despesa;;;;-0,05;BRL;;;;;;\r\n", b.String())
}

func TestTransactionsCSVEnUS(t *testing.T) {
	c, txs := csvFixture(t, i18n.EnUS)
	var b strings.Builder
	require.NoError(t, c.Write(&b, seqOf(txs[:1], nil)))
	assert.Equal(t, "\ufeff"+
		"Date,Type,Description,Category,Account,Amount,Currency,Destination account,Destination amount,Destination currency,Tags,Installment,Card invoice\r\n"+
		"2026-11-08,Expense,'-1+1 Café,Groceries,'=Cartão,-1234.56,BRL,,,,\"'@home, trip\",3/10,2026-12\r\n", b.String(),
		"a default category in the locale; a cell holding the delimiter is quoted")
}

func TestTransactionsCSVEsESAndFallback(t *testing.T) {
	c, txs := csvFixture(t, i18n.EsES)
	var b strings.Builder
	require.NoError(t, c.Write(&b, seqOf(txs[:1], nil)))
	lines := strings.Split(b.String(), "\r\n")
	assert.True(t, strings.HasPrefix(lines[0], "\ufeffFecha;Tipo;Descripción;"))
	assert.Equal(t, "08/11/2026;Gasto;'-1+1 Café;Supermercado;'=Cartão;-1234,56;BRL;;;;'@home, trip;3/10;12/2026", lines[1])

	c.Locale = "fr-FR"
	b.Reset()
	require.NoError(t, c.Write(&b, seqOf(nil, nil)))
	assert.True(t, strings.HasPrefix(b.String(), "\ufeffData;Tipo;"), "an unknown locale writes the default's")
}

func TestTransactionsCSVErrors(t *testing.T) {
	c, txs := csvFixture(t, i18n.EnUS)
	boom := errors.New("boom")
	var b strings.Builder
	assert.ErrorIs(t, c.Write(&b, seqOf(nil, boom)), boom)
	assert.Zero(t, b.Len(), "a first read that fails writes nothing, so the handler can still answer a problem")
	assert.ErrorIs(t, c.Write(&b, seqOf(txs[:2], boom)), boom, "a failed read ends the file")

	assert.ErrorIs(t, c.Write(failingWriter{}, seqOf(txs, nil)), errWrite)
}

var errWrite = errors.New("write failed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

func TestCSVText(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		"Café":              "Café",
		"=HYPERLINK(\"x\")": "'=HYPERLINK(\"x\")",
		"+55 11":            "'+55 11",
		"-1+1":              "'-1+1",
		"@SUM(A1)":          "'@SUM(A1)",
		"\tx":               "'\tx",
		"\rx":               "'\rx",
		"a=b":               "a=b",
	} {
		assert.Equal(t, want, csvText(in), "%q", in)
	}
}

func TestFormatMinor(t *testing.T) {
	assert.Equal(t, "0.00", formatMinor(0, '.'))
	assert.Equal(t, "0,07", formatMinor(7, ','))
	assert.Equal(t, "-0,70", formatMinor(-70, ','))
	assert.Equal(t, "1234567.89", formatMinor(123456789, '.'), "no thousands separator")
	assert.Equal(t, "-100000000000.00", formatMinor(-domain.MaxAmount, '.'))
}

func TestTransactionsCSVDates(t *testing.T) {
	// A date is a civil day: it never shifts with a time zone.
	c, _ := csvFixture(t, i18n.PtBR)
	tx := domain.Transaction{Kind: domain.TransactionIncome, Date: domain.DateOf(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))}
	row := c.row(csvLayouts[i18n.PtBR], tx)
	assert.Equal(t, "01/01/2026", row[0])
}
