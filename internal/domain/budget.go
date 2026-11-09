package domain

import (
	"cmp"
	"slices"
	"time"
)

// Bounds and defaults of budgets.
const (
	// DefaultBudgetWarnAt is the share of the limit, in percent, at which a
	// budget warns when its owner names none.
	DefaultBudgetWarnAt = 80
	// MinBudgetWarnAt and MaxBudgetWarnAt bound WarnAt.
	MinBudgetWarnAt = 1
	MaxBudgetWarnAt = 100
	// BudgetRollWindow bounds how many months one carry-over fills: someone
	// back after a longer gap gets the budgets of the last month done in
	// the latest BudgetRollWindow months (the months in between stay as
	// they were).
	BudgetRollWindow = 12
)

// BudgetStatus tells how much of its limit a budget has spent. The alerts
// of a budget are named by the status that triggers them.
type BudgetStatus string

// Budget statuses.
const (
	// BudgetOK is spending below the warning share of the limit.
	BudgetOK BudgetStatus = "ok"
	// BudgetWarning is spending from the warning share up to the limit,
	// the limit itself included.
	BudgetWarning BudgetStatus = "warning"
	// BudgetExceeded is spending past the limit.
	BudgetExceeded BudgetStatus = "exceeded"
)

// Budget is the most the owner means to spend on one expense category in
// one month (§1 item 6), in one currency: only the expenses in that
// currency count. The budgets of a month carry on to the next one when it
// starts (RollBudgets), so a change applies from its month on, and the
// months gone by keep theirs.
type Budget struct {
	ID         string
	UserID     string
	CategoryID string
	// Month, Currency and CategoryID never change.
	Month    Month
	Currency string
	// Limit is in minor units of Currency, above zero.
	Limit int64
	// WarnAt is the share of Limit, in percent, from which the budget warns.
	WarnAt int
	// WarnedAt and ExceededAt mark the alerts already given (or that need
	// none, as the state the owner set the budget in): each alert goes out
	// once per budget.
	WarnedAt   *time.Time
	ExceededAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// BudgetInput is a new budget as the owner describes it.
type BudgetInput struct {
	CategoryID string
	// Month is "YYYY-MM"; empty is the reader's month.
	Month    string
	Currency string
	Limit    int64
	// WarnAt is nil for DefaultBudgetWarnAt.
	WarnAt *int
}

// NewBudget validates in and returns the budget of userID for a reader
// whose month is current, or a *ValidationError naming every invalid
// field. A month after current is out of range: a month gets its budgets
// when it starts, carried from the one before (RollBudgets), which is what
// makes a change apply from its month on. Whether CategoryID names an
// expense category of the owner is the use case's to check.
func NewBudget(userID string, in BudgetInput, current Month, now time.Time) (Budget, error) {
	var v Validation
	if in.CategoryID == "" {
		v.Add("categoryId", CodeRequired)
	}

	month := current
	if in.Month != "" {
		m, err := ParseMonth(in.Month)
		switch {
		case err != nil:
			v.Check("month", err)
		case current.Before(m):
			v.Add("month", CodeOutOfRange)
		default:
			month = m
		}
	}

	currency, ok := ParseCurrency(in.Currency)
	switch {
	case in.Currency == "":
		v.Add("currency", CodeRequired)
	case !ok:
		v.Add("currency", CodeInvalid)
	}

	v.Check("limit", checkLimit(in.Limit))
	warnAt := DefaultBudgetWarnAt
	if in.WarnAt != nil {
		warnAt = *in.WarnAt
		v.Check("warnAt", checkWarnAt(warnAt))
	}
	if err := v.Err(); err != nil {
		return Budget{}, err
	}

	now = now.UTC()
	return Budget{
		UserID: userID, CategoryID: in.CategoryID, Month: month, Currency: currency,
		Limit: in.Limit, WarnAt: warnAt, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// checkLimit validates a budget limit: above zero, at most MaxAmount.
func checkLimit(v int64) error {
	if v <= 0 {
		return &fieldErr{CodeOutOfRange}
	}
	return CheckAmount(v, false)
}

// checkWarnAt validates the warning share of a budget, in percent.
func checkWarnAt(v int) error {
	if v < MinBudgetWarnAt || v > MaxBudgetWarnAt {
		return &fieldErr{CodeOutOfRange}
	}
	return nil
}

// BudgetPatch changes the limit or the warning share of a budget; nil
// fields stay as they are. The category, the month and the currency are not
// fields: they never change.
type BudgetPatch struct {
	Limit  *int64
	WarnAt *int
}

// Apply checks the patch and returns current with the changes, whether
// anything changed (UpdatedAt moves only then), or a *ValidationError
// naming each invalid field. The alert marks are the caller's to set again
// (Rearmed), from what the budget has spent.
func (p BudgetPatch) Apply(current Budget, now time.Time) (Budget, bool, error) {
	var v Validation
	next := current
	if p.Limit != nil {
		v.Check("limit", checkLimit(*p.Limit))
		next.Limit = *p.Limit
	}
	if p.WarnAt != nil {
		v.Check("warnAt", checkWarnAt(*p.WarnAt))
		next.WarnAt = *p.WarnAt
	}
	if err := v.Err(); err != nil {
		return Budget{}, false, err
	}
	if next.Limit == current.Limit && next.WarnAt == current.WarnAt {
		return current, false, nil
	}
	next.UpdatedAt = now.UTC()
	return next, true, nil
}

// Status returns the status of the budget once it has spent spent: past
// the limit it is exceeded; from WarnAt percent of it (the limit itself
// included) it warns.
func (b Budget) Status(spent int64) BudgetStatus {
	switch {
	case spent > b.Limit:
		return BudgetExceeded
	case spent >= b.warnThreshold():
		return BudgetWarning
	default:
		return BudgetOK
	}
}

// warnThreshold is the least amount spent at which the budget warns: WarnAt
// percent of the limit, rounded up. Limit * WarnAt stays below 10^16, well
// inside an int64, while a month's spending times 100 might not.
func (b Budget) warnThreshold() int64 {
	return (b.Limit*int64(b.WarnAt) + 99) / 100
}

// Rearmed returns the budget with its alert marks set to the state spent
// puts it in, at now: the alerts already crossed need none, and those not
// crossed (a limit raised) go out when spending crosses them. Creating a
// budget and changing its limit or its share are silent, since the owner is
// looking at it then.
func (b Budget) Rearmed(spent int64, now time.Time) Budget {
	status := b.Status(spent)
	b.WarnedAt = mark(b.WarnedAt, status != BudgetOK, now)
	b.ExceededAt = mark(b.ExceededAt, status == BudgetExceeded, now)
	return b
}

// mark keeps the mark at, sets it at now when crossed and it was unset, and
// clears it when not crossed.
func mark(at *time.Time, crossed bool, now time.Time) *time.Time {
	switch {
	case !crossed:
		return nil
	case at != nil:
		return at
	default:
		t := now.UTC()
		return &t
	}
}

// DueAlert returns the alert the budget still has to give once it has spent
// spent: exceeded before warning, since an exceeded budget has no use for
// the warning anymore. Spending less later rearms nothing.
func (b Budget) DueAlert(spent int64) (BudgetStatus, bool) {
	switch status := b.Status(spent); {
	case status == BudgetExceeded && b.ExceededAt == nil:
		return BudgetExceeded, true
	case status == BudgetWarning && b.WarnedAt == nil:
		return BudgetWarning, true
	default:
		return "", false
	}
}

// RollBudgets returns the budgets of from carried on to month into, at now:
// the same categories, currencies, limits and shares, with no id and no
// alert marks, so a new month that starts already past a share (an
// installment bought before) warns once.
func RollBudgets(from []Budget, into Month, now time.Time) []Budget {
	now = now.UTC()
	out := make([]Budget, len(from))
	for i, b := range from {
		out[i] = Budget{
			UserID: b.UserID, CategoryID: b.CategoryID, Month: into, Currency: b.Currency,
			Limit: b.Limit, WarnAt: b.WarnAt, CreatedAt: now, UpdatedAt: now,
		}
	}
	return out
}

// BudgetUsage is a budget with what it spent and what the recurrences are
// still to spend on it in its month.
type BudgetUsage struct {
	Budget Budget
	// Spent adds up the expenses of the category in the budget's currency
	// over the whole month, the days still to come included (an
	// installment later in the month), as the dashboard counts them.
	Spent int64
	// Scheduled is what the recurrences are still to create in the month;
	// it never counts toward Status.
	Scheduled int64
}

// Remaining is what is left of the limit, negative past it.
func (u BudgetUsage) Remaining() int64 { return u.Budget.Limit - u.Spent }

// Status is the status of the budget on what it spent.
func (u BudgetUsage) Status() BudgetStatus { return u.Budget.Status(u.Spent) }

// budgetKey groups spending the way a budget counts it.
type budgetKey struct{ category, currency string }

// UseBudgets returns the usage of each budget of month, in order, from the
// sums of the expenses (flows; income, other months and other currencies
// are left out) and what the recurrences are still to create (scheduled).
func UseBudgets(budgets []Budget, month Month, flows []FlowSum, scheduled []Transaction) []BudgetUsage {
	spent := make(map[budgetKey]int64)
	for _, f := range flows {
		if f.Kind == TransactionExpense && f.Month == month {
			spent[budgetKey{f.CategoryID, f.Currency}] += f.Amount
		}
	}
	planned := make(map[budgetKey]int64)
	for _, t := range scheduled {
		if t.Kind == TransactionExpense && MonthOf(t.Date) == month {
			planned[budgetKey{t.CategoryID, t.Currency}] += t.Amount
		}
	}
	out := make([]BudgetUsage, len(budgets))
	for i, b := range budgets {
		k := budgetKey{b.CategoryID, b.Currency}
		out[i] = BudgetUsage{Budget: b, Spent: spent[k], Scheduled: planned[k]}
	}
	return out
}

// BudgetTotal adds up the budgets of a month in one currency.
type BudgetTotal struct {
	Currency                string
	Limit, Spent, Scheduled int64
}

// Remaining is what is left of the limits, negative past them.
func (t BudgetTotal) Remaining() int64 { return t.Limit - t.Spent }

// TotalBudgets adds up usages by currency, in alphabetical order.
func TotalBudgets(usages []BudgetUsage) []BudgetTotal {
	byCurrency := make(map[string]*BudgetTotal)
	for _, u := range usages {
		t := byCurrency[u.Budget.Currency]
		if t == nil {
			t = &BudgetTotal{Currency: u.Budget.Currency}
			byCurrency[u.Budget.Currency] = t
		}
		t.Limit += u.Budget.Limit
		t.Spent += u.Spent
		t.Scheduled += u.Scheduled
	}
	out := make([]BudgetTotal, 0, len(byCurrency))
	for _, t := range byCurrency {
		out = append(out, *t)
	}
	slices.SortFunc(out, func(a, b BudgetTotal) int { return cmp.Compare(a.Currency, b.Currency) })
	return out
}

// BudgetMonth is the budgets of one month with their usage.
type BudgetMonth struct {
	Month Month
	// Projected tells a month still to start: its budgets are those of the
	// reader's month, with no ids, since they are created when it starts.
	Projected bool
	Budgets   []BudgetUsage
	Totals    []BudgetTotal
}
