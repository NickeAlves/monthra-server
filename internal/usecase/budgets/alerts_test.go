package budgets

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/email"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

type fakeUsers struct {
	users map[string]domain.User
	err   error
}

func (f *fakeUsers) ByID(_ context.Context, id string) (domain.User, error) {
	if f.err != nil {
		return domain.User{}, f.err
	}
	u, ok := f.users[id]
	if !ok {
		return domain.User{}, domain.ErrNotFound
	}
	return u, nil
}

type fakeMailer struct {
	mu   sync.Mutex
	sent []email.Message
}

func (f *fakeMailer) Send(_ context.Context, m email.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
}

type countingMetrics struct {
	mu     sync.Mutex
	alerts map[string]int
	errors int
}

func (m *countingMetrics) BudgetAlert(level, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts[level+" "+result]++
}

func (m *countingMetrics) BudgetAlertError() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errors++
}

type alertsFixture struct {
	alerts  *Alerts
	store   *memStore
	marker  *memMarker
	ledger  *fakeLedger
	users   *fakeUsers
	mailer  *fakeMailer
	metrics *countingMetrics
	logs    *bytes.Buffer
	now     *time.Time
}

func newAlertsFixture() *alertsFixture {
	now := t0
	verified := t0.Add(-24 * time.Hour)
	ana := domain.NewUser("Ana", "ana@example.com", "hash", i18n.EnUS, t0)
	ana.ID, ana.EmailVerifiedAt = "u1", &verified
	f := &alertsFixture{
		store: newMemStore(), marker: newMemMarker(), ledger: &fakeLedger{},
		users:  &fakeUsers{users: map[string]domain.User{"u1": ana}},
		mailer: &fakeMailer{}, metrics: &countingMetrics{alerts: map[string]int{}},
		logs: &bytes.Buffer{}, now: &now,
	}
	f.alerts = NewAlerts(f.store, f.marker, f.ledger, f.users, f.mailer, AlertsOptions{
		Now: func() time.Time { return *f.now }, Metrics: f.metrics,
		Logger: slog.New(slog.NewTextHandler(f.logs, nil)), AppURL: "https://app.monthra.example",
	})
	// November is carried over already.
	f.marker.through["u1"] = november
	return f
}

// budget stores a budget of u1 in m.
func (f *alertsFixture) budget(t *testing.T, m domain.Month, category string, limit int64) domain.Budget {
	t.Helper()
	b, err := domain.NewBudget("u1", domain.BudgetInput{CategoryID: category, Currency: "BRL", Limit: limit, Month: m.String()}, m, t0)
	require.NoError(t, err)
	b, err = f.store.Insert(context.Background(), b)
	require.NoError(t, err)
	return b
}

func (f *alertsFixture) stored(t *testing.T, id string) domain.Budget {
	t.Helper()
	b, err := f.store.ByID(context.Background(), "u1", id)
	require.NoError(t, err)
	return b
}

func TestCheckWithNothingPending(t *testing.T) {
	f := newAlertsFixture()
	f.alerts.Check(context.Background(), "u1")
	assert.Equal(t, 1, f.store.pending, "one month, one query")
	assert.Empty(t, f.ledger.calls, "nothing pending: no sums")
	assert.Empty(t, f.mailer.sent)

	// A budget already exceeded is not pending anymore.
	b := f.budget(t, november, "food", 1000)
	_, err := f.store.ClaimAlert(context.Background(), "u1", b.ID, domain.BudgetExceeded, t0)
	require.NoError(t, err)
	f.ledger.spend(november, "food", 5000)
	f.alerts.Check(context.Background(), "u1")
	assert.Empty(t, f.ledger.calls)
	assert.Empty(t, f.mailer.sent)
}

func TestCheckGivesEachAlertOnce(t *testing.T) {
	ctx := context.Background()
	f := newAlertsFixture()
	b := f.budget(t, november, "food", 1000)

	f.ledger.spend(november, "food", 700)
	f.alerts.Check(ctx, "u1")
	assert.Empty(t, f.mailer.sent, "below the warning")
	assert.Equal(t, []string{"u1 2026-11-01 2026-11-30"}, f.ledger.calls)

	f.ledger.spend(november, "food", 100) // 800: the warning
	f.alerts.Check(ctx, "u1")
	require.Len(t, f.mailer.sent, 1)
	assert.Equal(t, email.Message{
		To: "ana@example.com", Locale: i18n.EnUS, Template: email.TemplateBudgetWarning,
		Link: "https://app.monthra.example/budgets?month=2026-11", Month: "2026-11",
	}, f.mailer.sent[0])
	require.NotNil(t, f.stored(t, b.ID).WarnedAt)
	assert.Equal(t, 1, f.metrics.alerts["warning sent"])

	f.ledger.spend(november, "food", 100) // 900: still a warning
	f.alerts.Check(ctx, "u1")
	assert.Len(t, f.mailer.sent, 1, "once")

	f.ledger.spend(november, "food", 101) // 1001: exceeded
	f.alerts.Check(ctx, "u1")
	require.Len(t, f.mailer.sent, 2)
	assert.Equal(t, email.TemplateBudgetExceeded, f.mailer.sent[1].Template)
	require.NotNil(t, f.stored(t, b.ID).ExceededAt)

	f.ledger.spend(november, "food", 5000)
	f.alerts.Check(ctx, "u1")
	assert.Len(t, f.mailer.sent, 2, "once")
	assert.Equal(t, map[string]int{"warning sent": 1, "exceeded sent": 1}, f.metrics.alerts)
	assert.Zero(t, f.metrics.errors)
}

func TestCheckSendsOneEmailAboutTheWorst(t *testing.T) {
	f := newAlertsFixture()
	food := f.budget(t, november, "food", 1000)
	fun := f.budget(t, november, "fun", 1000)
	f.ledger.spend(november, "food", 850)
	f.ledger.spend(november, "fun", 2000)

	f.alerts.Check(context.Background(), "u1")
	require.Len(t, f.mailer.sent, 1)
	assert.Equal(t, email.TemplateBudgetExceeded, f.mailer.sent[0].Template)
	assert.NotNil(t, f.stored(t, food.ID).WarnedAt)
	assert.Nil(t, f.stored(t, food.ID).ExceededAt)
	assert.NotNil(t, f.stored(t, fun.ID).WarnedAt, "exceeding marks the warning too")
	assert.NotNil(t, f.stored(t, fun.ID).ExceededAt)
	assert.Equal(t, map[string]int{"warning sent": 1, "exceeded sent": 1}, f.metrics.alerts)
}

func TestCheckCountsTheBudgetCurrencyOnly(t *testing.T) {
	f := newAlertsFixture()
	f.budget(t, november, "food", 1000)
	f.ledger.flows = []domain.FlowSum{
		{Month: november, Currency: "USD", Kind: domain.TransactionExpense, CategoryID: "food", Amount: 5000},
		{Month: november, Currency: "BRL", Kind: domain.TransactionIncome, CategoryID: "food", Amount: 5000},
	}
	f.alerts.Check(context.Background(), "u1")
	assert.Empty(t, f.mailer.sent)
}

func TestCheckMutedAlerts(t *testing.T) {
	for name, mute := range map[string]func(*domain.User){
		"preference off": func(u *domain.User) { u.Preferences.BudgetAlerts = false },
		"unverified":     func(u *domain.User) { u.EmailVerifiedAt = nil },
	} {
		f := newAlertsFixture()
		u := f.users.users["u1"]
		mute(&u)
		f.users.users["u1"] = u
		b := f.budget(t, november, "food", 1000)
		f.ledger.spend(november, "food", 900)

		f.alerts.Check(context.Background(), "u1")
		assert.Empty(t, f.mailer.sent, name)
		assert.NotNil(t, f.stored(t, b.ID).WarnedAt, "%s: the alert stays given, so unmuting sends no old one", name)
		assert.Equal(t, map[string]int{"warning muted": 1}, f.metrics.alerts, name)
	}
}

func TestCheckFailsOpen(t *testing.T) {
	ctx := context.Background()
	f := newAlertsFixture()
	f.budget(t, november, "food", 1000)
	f.ledger.spend(november, "food", 900)

	f.store.listErr = errBoom
	f.alerts.Check(ctx, "u1")
	assert.Equal(t, 1, f.metrics.errors)
	assert.Contains(t, f.logs.String(), "budget alert check failed")
	f.store.listErr = nil

	f.ledger.err = errBoom
	f.alerts.Check(ctx, "u1")
	assert.Equal(t, 2, f.metrics.errors)
	f.ledger.err = nil

	f.users.err = errBoom
	f.alerts.Check(ctx, "u1")
	assert.Equal(t, 3, f.metrics.errors)
	assert.Empty(t, f.mailer.sent, "claimed, but the owner could not be read: the alert is lost, and logged")
	f.users.err = nil

	f.marker.err = errBoom
	f.alerts.Check(ctx, "u1")
	assert.Equal(t, 4, f.metrics.errors)
	f.marker.err = nil

	// An account being deleted gets no email and no error.
	f2 := newAlertsFixture()
	delete(f2.users.users, "u1")
	f2.budget(t, november, "food", 1000)
	f2.ledger.spend(november, "food", 900)
	f2.alerts.Check(ctx, "u1")
	assert.Empty(t, f2.mailer.sent)
	assert.Zero(t, f2.metrics.errors)
}

// failingClaims fails the claims after the first.
type failingClaims struct{ *memStore }

func (s failingClaims) ClaimAlert(ctx context.Context, userID, id string, status domain.BudgetStatus, at time.Time) (bool, error) {
	if s.claims > 0 {
		return false, errBoom
	}
	return s.memStore.ClaimAlert(ctx, userID, id, status, at)
}

func TestCheckSendsWhatItClaimedBeforeAFailure(t *testing.T) {
	f := newAlertsFixture()
	f.budget(t, november, "food", 1000)
	f.budget(t, november, "fun", 1000)
	f.ledger.spend(november, "food", 900)
	f.ledger.spend(november, "fun", 900)
	alerts := NewAlerts(failingClaims{f.store}, f.marker, f.ledger, f.users, f.mailer, AlertsOptions{
		Now: func() time.Time { return t0 }, Metrics: f.metrics,
	})
	alerts.Check(context.Background(), "u1")
	assert.Len(t, f.mailer.sent, 1, "the first alert claimed goes out")
	assert.Equal(t, 1, f.metrics.errors)
}

func TestCheckConcurrently(t *testing.T) {
	f := newAlertsFixture()
	f.budget(t, november, "food", 1000)
	f.ledger.spend(november, "food", 2000)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { f.alerts.Check(context.Background(), "u1") })
	}
	wg.Wait()
	assert.Len(t, f.mailer.sent, 1)
}

func TestCheckRunsAfterTheRequestIsGone(t *testing.T) {
	f := newAlertsFixture()
	f.budget(t, november, "food", 1000)
	f.ledger.spend(november, "food", 900)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.alerts.Check(ctx, "u1")
	assert.Len(t, f.mailer.sent, 1)
}

func TestCheckCarriesTheMonthOver(t *testing.T) {
	f := newAlertsFixture()
	october := november.AddMonths(-1)
	f.marker.through["u1"] = october
	f.budget(t, october, "food", 1000)
	f.ledger.spend(november, "food", 950) // an installment bought in October

	f.alerts.Check(context.Background(), "u1")
	assert.Equal(t, november, f.marker.through["u1"])
	assert.Equal(t, []string{"2026-10", "2026-11"}, f.store.months("u1"))
	require.Len(t, f.mailer.sent, 1, "a month that starts past the warning alerts")
	assert.Equal(t, "2026-11", f.mailer.sent[0].Month)
}

func TestCheckAroundTheTurnOfTheMonth(t *testing.T) {
	f := newAlertsFixture()
	december := november.AddMonths(1)

	// The last hour of November in UTC: a reader in December may already
	// have its budgets carried over, and they are checked too; December is
	// never carried over by the check itself.
	*f.now = time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC)
	f.alerts.Check(context.Background(), "u1")
	assert.Equal(t, november, f.marker.through["u1"])
	assert.Equal(t, 2, f.store.pending, "November and December")

	f.budget(t, november, "food", 1000)
	f.budget(t, december, "food", 1000)
	f.ledger.spend(november, "food", 900)
	f.ledger.spend(december, "food", 2000)
	f.alerts.Check(context.Background(), "u1")
	require.Len(t, f.mailer.sent, 2, "one email per month")
	assert.Equal(t, "2026-11", f.mailer.sent[0].Month)
	assert.Equal(t, email.TemplateBudgetWarning, f.mailer.sent[0].Template)
	assert.Equal(t, "2026-12", f.mailer.sent[1].Month)
	assert.Equal(t, email.TemplateBudgetExceeded, f.mailer.sent[1].Template)

	// Mid-month, one month is checked.
	f.store.pending = 0
	*f.now = t0
	f.alerts.Check(context.Background(), "u1")
	assert.Equal(t, 1, f.store.pending)
}
