package budgets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/email"
)

// checkTimeout bounds one alert check, which runs after a write even when
// the request that wrote is gone.
const checkTimeout = 3 * time.Second

// Alert results, for the metrics.
const (
	resultSent  = "sent"
	resultMuted = "muted"
)

// Users finds the owner of the budgets, to email the alerts.
type Users interface {
	ByID(ctx context.Context, id string) (domain.User, error)
}

// Mailer queues transactional emails; it never blocks.
type Mailer interface {
	Send(ctx context.Context, m email.Message)
}

// Metrics counts the alerts and the failed checks.
type Metrics interface {
	BudgetAlert(level, result string)
	BudgetAlertError()
}

type noMetrics struct{}

func (noMetrics) BudgetAlert(string, string) {}
func (noMetrics) BudgetAlertError()          {}

// AlertsOptions tunes the Alerts.
type AlertsOptions struct {
	// Now defaults to time.Now.
	Now func() time.Time
	// Logger records the failed checks; it defaults to none.
	Logger *slog.Logger
	// Metrics defaults to counting nothing.
	Metrics Metrics
	// AppURL is the web app base URL of the link in the emails.
	AppURL config.PublicURL
}

// Alerts gives the budget alerts: after every write to a user's finance
// data, the budgets of the month that crossed their warning share or their
// limit get their alert, once each (a compare-and-set on its mark), and the
// owner one email, about the worst of them, unless they muted the alerts.
// It is built before the use cases that write, and called through their
// WriteHook.
type Alerts struct {
	store   Store
	roll    roller
	ledger  Ledger
	users   Users
	mailer  Mailer
	metrics Metrics
	logger  *slog.Logger
	appURL  config.PublicURL
	now     func() time.Time
}

// NewAlerts returns the alerts on the budgets of store.
func NewAlerts(store Store, marker Marker, ledger Ledger, users Users, mailer Mailer, o AlertsOptions) *Alerts {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Metrics == nil {
		o.Metrics = noMetrics{}
	}
	return &Alerts{
		store: store, roll: roller{store: store, marker: marker, now: o.Now}, ledger: ledger,
		users: users, mailer: mailer, metrics: o.Metrics, logger: o.Logger, appURL: o.AppURL, now: o.Now,
	}
}

// Check gives the alerts of userID's budgets that a write may have made
// due. It looks at the months of the day before and after today in UTC
// (every time zone's month): a write to the transactions of another month
// gives no alert. The month of today in UTC is carried over first, so a
// new month alerts without anyone opening its budgets. It never creates
// occurrences (that runs the hooks again) and never fails: a failure is
// logged and counted, and the alert waits for the next check.
func (a *Alerts) Check(ctx context.Context, userID string) {
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
	defer cancel()
	if err := a.check(checkCtx, userID); err != nil {
		a.metrics.BudgetAlertError()
		a.logger.WarnContext(ctx, "budget alert check failed", "error", err)
	}
}

func (a *Alerts) check(ctx context.Context, userID string) error {
	today := domain.DateOf(a.now().UTC())
	if err := a.roll.ensure(ctx, userID, domain.MonthOf(today)); err != nil {
		return err
	}
	var months []domain.Month
	for _, d := range []domain.Date{today.AddDays(-1), today, today.AddDays(1)} {
		if m := domain.MonthOf(d); len(months) == 0 || months[len(months)-1] != m {
			months = append(months, m)
		}
	}
	for _, m := range months {
		if err := a.checkMonth(ctx, userID, m); err != nil {
			return err
		}
	}
	return nil
}

// checkMonth gives the alerts due in month m. A budget exceeded already
// has given both, so only the others are read; with none (the usual
// case), that is the only query.
func (a *Alerts) checkMonth(ctx context.Context, userID string, m domain.Month) error {
	pending, err := a.store.Pending(ctx, userID, m)
	if err != nil {
		return fmt.Errorf("list pending budgets: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}
	flows, err := a.ledger.Flows(ctx, userID, m.Day(1), m.LastDay())
	if err != nil {
		return fmt.Errorf("sum flows: %w", err)
	}
	at := clock(a.now)
	var claimed []domain.BudgetStatus
	var errs []error
	for _, u := range domain.UseBudgets(pending, m, flows, nil) {
		due, ok := u.Budget.DueAlert(u.Spent)
		if !ok {
			continue
		}
		won, err := a.store.ClaimAlert(ctx, userID, u.Budget.ID, due, at)
		if err != nil {
			// The alerts claimed so far still go out below.
			errs = append(errs, fmt.Errorf("claim budget alert: %w", err))
			break
		}
		if won {
			claimed = append(claimed, due)
		}
	}
	if len(claimed) > 0 {
		errs = append(errs, a.notify(ctx, userID, m, claimed))
	}
	return errors.Join(errs...)
}

// notify emails the owner about the worst of the alerts claimed in month
// m, unless the owner muted them (the alerts stay given, so unmuting sends
// none of the old ones).
func (a *Alerts) notify(ctx context.Context, userID string, m domain.Month, claimed []domain.BudgetStatus) error {
	u, err := a.users.ByID(ctx, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil // the account is being deleted
	}
	if err != nil {
		return fmt.Errorf("find user: %w", err)
	}
	result := resultMuted
	if u.Preferences.BudgetAlerts && u.EmailVerified() {
		template := email.TemplateBudgetWarning
		for _, s := range claimed {
			if s == domain.BudgetExceeded {
				template = email.TemplateBudgetExceeded
			}
		}
		a.mailer.Send(ctx, email.Message{
			To: u.Email, Locale: u.Preferences.Locale, Template: template,
			Link: a.appURL.Join("/budgets?month=" + m.String()), Month: m.String(),
		})
		result = resultSent
	}
	for _, s := range claimed {
		a.metrics.BudgetAlert(string(s), result)
	}
	return nil
}
