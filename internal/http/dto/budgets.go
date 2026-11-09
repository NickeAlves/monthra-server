package dto

import (
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// CreateBudgetRequest is the body of POST /budgets. The domain checks every
// field, so missing ones are reported there.
type CreateBudgetRequest struct {
	CategoryID string `json:"categoryId"`
	// Month is "YYYY-MM"; empty is the reader's month (?asOf).
	Month    string `json:"month"`
	Currency string `json:"currency"`
	Limit    int64  `json:"limit"`
	WarnAt   *int   `json:"warnAt"`
}

// Input maps the request to the domain input.
func (r CreateBudgetRequest) Input() domain.BudgetInput {
	return domain.BudgetInput{CategoryID: r.CategoryID, Month: r.Month, Currency: r.Currency, Limit: r.Limit, WarnAt: r.WarnAt}
}

// UpdateBudgetRequest is the body of PATCH /budgets/{id}; absent fields
// stay as they are. The category, the month and the currency are not
// fields: they never change.
type UpdateBudgetRequest struct {
	Limit  *int64 `json:"limit"`
	WarnAt *int   `json:"warnAt"`
}

// Patch maps the request to the domain patch.
func (r UpdateBudgetRequest) Patch() domain.BudgetPatch {
	return domain.BudgetPatch{Limit: r.Limit, WarnAt: r.WarnAt}
}

// Budget is the public view of a budget with its usage. Amounts are
// integers in minor units (cents) of its currency; the category comes as an
// id (its name, icon and color come from /categories).
type Budget struct {
	// ID is null on a projected budget (a month still to start).
	ID         *string `json:"id"`
	CategoryID string  `json:"categoryId"`
	Month      string  `json:"month"`
	Currency   string  `json:"currency"`
	Limit      int64   `json:"limit"`
	// WarnAt is the share of the limit, in percent, from which it warns.
	WarnAt int `json:"warnAt"`
	// Spent adds up the category's expenses in the currency over the whole
	// month; Scheduled is what the recurrences are still to create in it.
	Spent     int64 `json:"spent"`
	Scheduled int64 `json:"scheduled"`
	// Remaining is Limit less Spent, negative past the limit.
	Remaining int64 `json:"remaining"`
	// Status is ok, warning (from WarnAt percent of the limit, the limit
	// itself included) or exceeded (past the limit), on Spent alone.
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// BudgetTotal adds up the budgets of a month in one currency.
type BudgetTotal struct {
	Currency  string `json:"currency"`
	Limit     int64  `json:"limit"`
	Spent     int64  `json:"spent"`
	Scheduled int64  `json:"scheduled"`
	Remaining int64  `json:"remaining"`
}

// Budgets is the body of GET /budgets: the budgets of one month, whole.
type Budgets struct {
	Month string `json:"month"`
	// Projected tells a month still to start: its budgets are the reader's
	// month's, with no ids, until it starts.
	Projected bool          `json:"projected"`
	Budgets   []Budget      `json:"budgets"`
	Totals    []BudgetTotal `json:"totals"`
}

// NewBudget maps a budget with its usage to its public view.
func NewBudget(u domain.BudgetUsage) Budget {
	b := u.Budget
	out := Budget{
		CategoryID: b.CategoryID, Month: b.Month.String(), Currency: b.Currency,
		Limit: b.Limit, WarnAt: b.WarnAt, Spent: u.Spent, Scheduled: u.Scheduled,
		Remaining: u.Remaining(), Status: string(u.Status()),
		CreatedAt: b.CreatedAt.UTC(), UpdatedAt: b.UpdatedAt.UTC(),
	}
	if b.ID != "" {
		out.ID = &b.ID
	}
	return out
}

// NewBudgets maps the budgets of a month to the body of GET /budgets.
func NewBudgets(m domain.BudgetMonth) Budgets {
	out := Budgets{
		Month: m.Month.String(), Projected: m.Projected,
		Budgets: make([]Budget, len(m.Budgets)), Totals: make([]BudgetTotal, len(m.Totals)),
	}
	for i, u := range m.Budgets {
		out.Budgets[i] = NewBudget(u)
	}
	for i, t := range m.Totals {
		out.Totals[i] = BudgetTotal{Currency: t.Currency, Limit: t.Limit, Spent: t.Spent, Scheduled: t.Scheduled, Remaining: t.Remaining()}
	}
	return out
}
