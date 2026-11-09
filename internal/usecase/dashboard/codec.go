package dashboard

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// codecVersion names the format of the cached summaries; it is part of
// their key, so a deploy that changes the format never reads an old one.
const codecVersion = "v1"

// summaryJSON is how a domain.Summary is cached.
type summaryJSON struct {
	Month      string         `json:"month"`
	AsOf       string         `json:"asOf"`
	Currencies []currencyJSON `json:"currencies"`
	Accounts   []accountJSON  `json:"accounts"`
}

type currencyJSON struct {
	Currency           string         `json:"currency"`
	Income             int64          `json:"income"`
	Expense            int64          `json:"expense"`
	Net                int64          `json:"net"`
	ScheduledIncome    int64          `json:"scheduledIncome"`
	ScheduledExpense   int64          `json:"scheduledExpense"`
	Balance            int64          `json:"balance"`
	ExpensesByCategory []categoryJSON `json:"expensesByCategory"`
	IncomeByCategory   []categoryJSON `json:"incomeByCategory"`
	History            []pointJSON    `json:"history"`
}

type categoryJSON struct {
	CategoryID string `json:"categoryId"`
	Amount     int64  `json:"amount"`
	Count      int    `json:"count"`
}

type pointJSON struct {
	Month    string `json:"month"`
	Income   int64  `json:"income"`
	Expense  int64  `json:"expense"`
	NetWorth *int64 `json:"netWorth"`
}

type accountJSON struct {
	AccountID string `json:"accountId"`
	Currency  string `json:"currency"`
	Balance   int64  `json:"balance"`
}

// encode writes s for the cache.
func encode(s domain.Summary) ([]byte, error) {
	out := summaryJSON{
		Month: s.Month.String(), AsOf: s.AsOf.String(),
		Currencies: make([]currencyJSON, len(s.Currencies)), Accounts: make([]accountJSON, len(s.Accounts)),
	}
	for i, c := range s.Currencies {
		cj := currencyJSON{
			Currency: c.Currency, Income: c.Income, Expense: c.Expense, Net: c.Net,
			ScheduledIncome: c.Scheduled.Income, ScheduledExpense: c.Scheduled.Expense, Balance: c.Balance,
			ExpensesByCategory: encodeTotals(c.ExpensesByCategory), IncomeByCategory: encodeTotals(c.IncomeByCategory),
			History: make([]pointJSON, len(c.History)),
		}
		for j, p := range c.History {
			cj.History[j] = pointJSON{Month: p.Month.String(), Income: p.Income, Expense: p.Expense, NetWorth: p.NetWorth}
		}
		out.Currencies[i] = cj
	}
	for i, a := range s.Accounts {
		out.Accounts[i] = accountJSON{AccountID: a.AccountID, Currency: a.Currency, Balance: a.Balance}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode summary: %w", err)
	}
	return b, nil
}

func encodeTotals(list []domain.CategoryTotal) []categoryJSON {
	out := make([]categoryJSON, len(list))
	for i, t := range list {
		out[i] = categoryJSON{CategoryID: t.CategoryID, Amount: t.Amount, Count: t.Count}
	}
	return out
}

// decode reads a summary encode wrote.
func decode(b []byte) (domain.Summary, error) {
	var in summaryJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return domain.Summary{}, fmt.Errorf("decode summary: %w", err)
	}
	month, err := parseMonth(in.Month)
	if err != nil {
		return domain.Summary{}, err
	}
	asOf, err := domain.ParseDate(in.AsOf)
	if err != nil {
		return domain.Summary{}, fmt.Errorf("decode summary day: %w", err)
	}
	out := domain.Summary{
		Month: month, AsOf: asOf,
		Currencies: make([]domain.CurrencySummary, len(in.Currencies)), Accounts: make([]domain.AccountBalance, len(in.Accounts)),
	}
	for i, c := range in.Currencies {
		cs := domain.CurrencySummary{
			Currency: c.Currency, Income: c.Income, Expense: c.Expense, Net: c.Net,
			Scheduled: domain.Flows{Income: c.ScheduledIncome, Expense: c.ScheduledExpense}, Balance: c.Balance,
			ExpensesByCategory: decodeTotals(c.ExpensesByCategory), IncomeByCategory: decodeTotals(c.IncomeByCategory),
			History: make([]domain.SummaryPoint, len(c.History)),
		}
		for j, p := range c.History {
			m, err := parseMonth(p.Month)
			if err != nil {
				return domain.Summary{}, err
			}
			cs.History[j] = domain.SummaryPoint{Month: m, Income: p.Income, Expense: p.Expense, NetWorth: p.NetWorth}
		}
		out.Currencies[i] = cs
	}
	for i, a := range in.Accounts {
		out.Accounts[i] = domain.AccountBalance{AccountID: a.AccountID, Currency: a.Currency, Balance: a.Balance}
	}
	return out, nil
}

func decodeTotals(list []categoryJSON) []domain.CategoryTotal {
	out := make([]domain.CategoryTotal, len(list))
	for i, t := range list {
		out[i] = domain.CategoryTotal{CategoryID: t.CategoryID, Amount: t.Amount, Count: t.Count}
	}
	return out
}

// parseMonth reads a month encode wrote; a history may reach before
// domain.MinMonth, which ParseMonth refuses.
func parseMonth(s string) (domain.Month, error) {
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return domain.Month{}, fmt.Errorf("decode summary month: %w", err)
	}
	return domain.NewMonth(t.Year(), t.Month()), nil
}
