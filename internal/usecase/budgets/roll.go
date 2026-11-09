package budgets

import (
	"context"
	"fmt"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// roller carries the budgets of each month on to the next one when it
// starts, lazily: before every read or write of budgets and every alert
// check, as the recurrences create their occurrences. A month gets the
// budgets of the one before (domain.RollBudgets), so a budget deleted or
// changed in a month is so from it on, and the months gone by keep theirs.
type roller struct {
	store  Store
	marker Marker
	now    func() time.Time
}

// ensure carries the caller's budgets on to every month after the last one
// done, through current, each from the month before. After a gap longer
// than domain.BudgetRollWindow, only the months of the window are filled,
// the first one from the last month done (the months in between stay as
// they were). The store skips the categories a month already has, so a
// pass that fails halfway is completed by the next one, and two concurrent
// passes store each budget once. (A budget deleted the instant its month
// starts, while another request carries it over, may come back: an
// accepted race of one user with themselves.)
func (r roller) ensure(ctx context.Context, userID string, current domain.Month) error {
	done, err := r.marker.BudgetsRolledThrough(ctx, userID)
	if err != nil {
		return fmt.Errorf("check budgets rollover: %w", err)
	}
	if !done.Before(current) {
		return nil
	}
	first, source := done.AddMonths(1), done
	if earliest := current.AddMonths(1 - domain.BudgetRollWindow); first.Before(earliest) {
		first = earliest
		if done.IsZero() {
			source = earliest.AddMonths(-1)
		}
	}
	at := clock(r.now)
	for m := first; !current.Before(m); m = m.AddMonths(1) {
		from, err := r.store.List(ctx, userID, source)
		if err != nil {
			return fmt.Errorf("list budgets to carry over: %w", err)
		}
		if err := r.store.InsertRolled(ctx, domain.RollBudgets(from, m, at)); err != nil {
			return fmt.Errorf("carry budgets over: %w", err)
		}
		source = m
	}
	if err := r.marker.MarkBudgetsRolled(ctx, userID, current); err != nil {
		return fmt.Errorf("mark budgets rollover: %w", err)
	}
	return nil
}

// clock is the current time at the precision MongoDB keeps (milliseconds),
// so what a write returns equals what later reads return.
func clock(now func() time.Time) time.Time { return now().UTC().Truncate(time.Millisecond) }
