package dto

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

func fieldErrors(t *testing.T, err error) []domain.FieldError {
	t.Helper()
	var ve *domain.ValidationError
	require.ErrorAs(t, err, &ve)
	return ve.Fields
}

func TestParseReportQuery(t *testing.T) {
	q, err := ParseReportQuery(url.Values{"from": {"2026-01"}, "to": {"2026-12"}, "asOf": {"2026-11-08"}})
	require.NoError(t, err)
	assert.Equal(t, ReportQuery{From: mustMonth(t, "2026-01"), To: mustMonth(t, "2026-12"), AsOf: mustDate(t, "2026-11-08")}, q)

	q, err = ParseReportQuery(url.Values{})
	require.NoError(t, err)
	assert.Equal(t, ReportQuery{}, q, "the use case picks the defaults")

	_, err = ParseReportQuery(url.Values{"from": {"2026-1"}, "to": {"x"}, "asOf": {"2026-11-31"}})
	assert.Equal(t, []domain.FieldError{
		{Field: "from", Code: domain.CodeInvalid}, {Field: "to", Code: domain.CodeInvalid}, {Field: "asOf", Code: domain.CodeInvalid},
	}, fieldErrors(t, err))
}

func TestParseExportQuery(t *testing.T) {
	q, err := ParseExportQuery(url.Values{
		"format": {"csv"}, "from": {"2026-01-01"}, "to": {"2026-12-31"}, "accountId": {"a1"}, "kind": {"expense"},
		"tag": {"Home"}, "q": {"Café"}, "asOf": {"2026-11-08"},
		// The page size and the cursor are the list's; an export reads it all.
		"limit": {"x"}, "cursor": {"x"},
	})
	require.NoError(t, err)
	assert.Equal(t, "csv", q.Format)
	assert.Equal(t, domain.TransactionFilter{
		From: mustDate(t, "2026-01-01"), To: mustDate(t, "2026-12-31"), AccountID: "a1", Kind: domain.TransactionExpense,
		Tag: "home", Search: "cafe",
	}, q.Filter)
	assert.Equal(t, mustDate(t, "2026-11-08"), q.AsOf)

	_, err = ParseExportQuery(url.Values{})
	assert.Equal(t, []domain.FieldError{{Field: "format", Code: domain.CodeRequired}}, fieldErrors(t, err))
	_, err = ParseExportQuery(url.Values{"format": {"pdf"}, "invoice": {"2026-11"}, "asOf": {"x"}})
	assert.Equal(t, []domain.FieldError{
		{Field: "accountId", Code: domain.CodeRequired}, {Field: "format", Code: domain.CodeInvalid}, {Field: "asOf", Code: domain.CodeInvalid},
	}, fieldErrors(t, err), "the PDF is the app's; an invoice needs its card")
}

func TestNewReportJSON(t *testing.T) {
	rate := int64(2500)
	worth := int64(1000)
	r := domain.Report{
		Period:   domain.Period{From: mustMonth(t, "2026-10"), To: mustMonth(t, "2026-11")},
		Previous: domain.Period{From: mustMonth(t, "2026-08"), To: mustMonth(t, "2026-09")},
		AsOf:     mustDate(t, "2026-11-08"),
		Currencies: []domain.CurrencyReport{{
			Currency:         "BRL",
			PeriodTotals:     domain.PeriodTotals{Income: 400, Expense: 300, Net: 100, SavingsRateBps: &rate},
			InvestmentIncome: 10,
			Months: []domain.ReportMonth{
				{Month: mustMonth(t, "2026-10"), Income: 400, Net: 400, NetWorth: &worth},
				{Month: mustMonth(t, "2026-11"), Expense: 300, Net: -300},
			},
			ExpensesByCategory: []domain.CategoryComparison{{CategoryID: "c1", Amount: 300, Count: 2, PreviousAmount: 50}},
			ByAccount:          []domain.AccountTotal{{AccountID: "a1", Income: 400, Expense: 300, Count: 3}},
			ExpensesByTag:      []domain.TagTotal{{Tag: "home", Amount: 300, Count: 2}},
		}},
	}
	b, err := json.Marshal(NewReport(r))
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"from": "2026-10", "to": "2026-11", "asOf": "2026-11-08",
		"previous": {"from": "2026-08", "to": "2026-09"},
		"currencies": [{
			"currency": "BRL", "income": 400, "expense": 300, "net": 100, "savingsRateBps": 2500, "investmentIncome": 10,
			"previous": {"income": 0, "expense": 0, "net": 0, "savingsRateBps": null},
			"months": [
				{"month": "2026-10", "income": 400, "expense": 0, "net": 400, "netWorth": 1000},
				{"month": "2026-11", "income": 0, "expense": 300, "net": -300, "netWorth": null}
			],
			"expensesByCategory": [{"categoryId": "c1", "amount": 300, "count": 2, "previousAmount": 50}],
			"incomeByCategory": [],
			"byAccount": [{"accountId": "a1", "income": 400, "expense": 300, "count": 3}],
			"expensesByTag": [{"tag": "home", "amount": 300, "count": 2}],
			"incomeByTag": []
		}]
	}`, string(b))

	b, err = json.Marshal(NewReport(domain.Report{}))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"currencies":[]`)
}
