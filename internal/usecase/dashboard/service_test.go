package dashboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// t0 is 2026-11-08 in UTC.
var t0 = time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)

func day(t *testing.T, s string) domain.Date {
	t.Helper()
	d, err := domain.ParseDate(s)
	require.NoError(t, err)
	return d
}

// calls records what the fakes were asked, in order.
type calls []string

func (c *calls) add(format string, args ...any) { *c = append(*c, fmt.Sprintf(format, args...)) }

type fakeAccounts struct {
	log  *calls
	list []domain.Account
	err  error
}

func (f *fakeAccounts) List(_ context.Context, userID string, includeArchived bool) ([]domain.Account, error) {
	f.log.add("accounts %s %t", userID, includeArchived)
	return f.list, f.err
}

type fakeLedger struct {
	log              *calls
	flows            []domain.FlowSum
	legs             []domain.LegSum
	flowErr, legsErr error
}

func (f *fakeLedger) Flows(_ context.Context, userID string, from, to domain.Date) ([]domain.FlowSum, error) {
	f.log.add("flows %s %s %s", userID, from, to)
	return f.flows, f.flowErr
}

func (f *fakeLedger) LegSums(_ context.Context, userID string, from domain.Month, through domain.Date) ([]domain.LegSum, error) {
	f.log.add("legs %s %s %s", userID, from, through)
	return f.legs, f.legsErr
}

type fakeRecurrences struct {
	log       *calls
	scheduled []domain.Transaction
	err       error
}

func (f *fakeRecurrences) Materialize(_ context.Context, userID string, asOf domain.Date) {
	f.log.add("materialize %s %s", userID, asOf)
}

func (f *fakeRecurrences) Scheduled(_ context.Context, userID string, from, to domain.Date) ([]domain.Transaction, error) {
	f.log.add("scheduled %s %s %s", userID, from, to)
	return f.scheduled, f.err
}

// memCache keeps entries by user, version and slot, as Redis does.
type memCache struct {
	log                            *calls
	versions                       map[string]int
	entries                        map[string][]byte
	versionErr, getErr, setErr     error
	bumpErr                        error
	bumpedWithCancelledCtx, bumped int
}

func newMemCache(log *calls) *memCache {
	return &memCache{log: log, versions: map[string]int{}, entries: map[string][]byte{}}
}

func (c *memCache) Version(_ context.Context, userID string) (string, error) {
	c.log.add("version %s", userID)
	if c.versionErr != nil {
		return "", c.versionErr
	}
	return fmt.Sprintf("v%d", c.versions[userID]), nil
}

func (c *memCache) Get(_ context.Context, userID, version, slot string) ([]byte, bool, error) {
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	b, ok := c.entries[userID+":"+version+":"+slot]
	return b, ok, nil
}

func (c *memCache) Set(_ context.Context, userID, version, slot string, value []byte) error {
	c.log.add("set %s %s %s", userID, version, slot)
	if c.setErr != nil {
		return c.setErr
	}
	c.entries[userID+":"+version+":"+slot] = value
	return nil
}

func (c *memCache) Bump(ctx context.Context, userID string) error {
	if ctx.Err() != nil {
		c.bumpedWithCancelledCtx++
	}
	if c.bumpErr != nil {
		return c.bumpErr
	}
	c.bumped++
	c.versions[userID]++
	return nil
}

type countingMetrics struct{ counts map[string]int }

func (m *countingMetrics) CacheRequest(cache, result string) { m.counts[cache+":"+result]++ }
func (m *countingMetrics) CacheError(cache, op string)       { m.counts[cache+":error:"+op]++ }

type fixture struct {
	svc         *Service
	log         *calls
	accounts    *fakeAccounts
	ledger      *fakeLedger
	recurrences *fakeRecurrences
	cache       *memCache
	metrics     *countingMetrics
	logs        *bytes.Buffer
}

func newFixture() fixture {
	log := &calls{}
	f := fixture{
		log: log,
		accounts: &fakeAccounts{log: log, list: []domain.Account{
			{ID: "bank", UserID: "u1", Currency: "BRL", InitialBalance: 100000},
		}},
		ledger: &fakeLedger{log: log,
			flows: []domain.FlowSum{{Month: domain.NewMonth(2026, time.November), Currency: "BRL", Kind: domain.TransactionExpense, CategoryID: "food", Amount: 3000, Count: 1}},
			legs:  []domain.LegSum{{AccountID: "bank", Month: domain.NewMonth(2026, time.November), Sum: -3000}},
		},
		recurrences: &fakeRecurrences{log: log},
		cache:       newMemCache(log),
		metrics:     &countingMetrics{counts: map[string]int{}},
		logs:        &bytes.Buffer{},
	}
	f.svc = NewService(f.accounts, f.ledger, f.recurrences, f.cache, Options{
		Now: func() time.Time { return t0 }, Metrics: f.metrics, Logger: slog.New(slog.NewJSONHandler(f.logs, nil)),
	})
	return f
}

func (f fixture) reset() { *f.log = nil }

func TestSummaryComputesThenCaches(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.recurrences.scheduled = []domain.Transaction{{Kind: domain.TransactionExpense, Currency: "BRL", Amount: 500, Date: day(t, "2026-11-20")}}

	sum, err := f.svc.Summary(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, "2026-11", sum.Month.String(), "the reader's month")
	assert.Equal(t, "2026-11-08", sum.AsOf.String())
	require.Len(t, sum.Currencies, 1)
	assert.Equal(t, int64(3000), sum.Currencies[0].Expense)
	assert.Equal(t, domain.Flows{Expense: 500}, sum.Currencies[0].Scheduled)
	assert.Equal(t, int64(97000), sum.Currencies[0].Balance)
	assert.Equal(t, calls{
		"materialize u1 2026-11-08",
		"version u1",
		"accounts u1 true",
		"flows u1 2025-12-01 2026-11-30",
		"legs u1 2025-12 2026-11-08",
		"scheduled u1 2026-11-01 2026-11-30",
		"set u1 v0 v1:2026-11:2026-11-08",
	}, *f.log, "the occurrences first, then the cache, then the sums")
	assert.Equal(t, 1, f.metrics.counts["summary:miss"])

	// The same summary again comes from the cache.
	f.reset()
	again, err := f.svc.Summary(ctx, "u1", domain.NewMonth(2026, time.November), day(t, "2026-11-08"))
	require.NoError(t, err)
	assert.Equal(t, sum, again)
	assert.Equal(t, calls{"materialize u1 2026-11-08", "version u1"}, *f.log)
	assert.Equal(t, 1, f.metrics.counts["summary:hit"])

	// A write bumps the version: computed again.
	NewInvalidator(f.cache, Options{}).Invalidate(ctx, "u1")
	f.reset()
	f.ledger.flows = nil
	fresh, err := f.svc.Summary(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	assert.Zero(t, fresh.Currencies[0].Expense)
	assert.Contains(t, *f.log, "set u1 v1 v1:2026-11:2026-11-08")

	// Another day of the reader is another entry.
	f.reset()
	_, err = f.svc.Summary(ctx, "u1", domain.Month{}, day(t, "2026-11-09"))
	require.NoError(t, err)
	assert.Contains(t, *f.log, "set u1 v1 v1:2026-11:2026-11-09")
}

func TestSummaryMonths(t *testing.T) {
	ctx := context.Background()
	f := newFixture()

	// A past month counts up to its last day, its history up to it.
	past, err := f.svc.Summary(ctx, "u1", domain.NewMonth(2026, time.March), day(t, "2026-11-07"))
	require.NoError(t, err)
	assert.Equal(t, "2026-03-31", past.AsOf.String())
	assert.Contains(t, *f.log, "materialize u1 2026-11-07", "the reader's today")
	assert.Contains(t, *f.log, "flows u1 2025-04-01 2026-03-31")
	assert.Contains(t, *f.log, "legs u1 2025-04 2026-03-31")

	// A day beyond the reader's today is held to a day of UTC's.
	f.reset()
	_, err = f.svc.Summary(ctx, "u1", domain.Month{}, day(t, "2027-01-01"))
	require.NoError(t, err)
	assert.Contains(t, *f.log, "materialize u1 2026-11-09")

	// Months farther than CacheWindow are computed every time.
	for _, m := range []domain.Month{domain.NewMonth(2025, time.October), domain.NewMonth(2027, time.December)} {
		f.reset()
		_, err = f.svc.Summary(ctx, "u1", m, domain.Date{})
		require.NoError(t, err)
		assert.NotContains(t, *f.log, "version u1", m.String())
		assert.NotContains(t, fmt.Sprint(*f.log), "set u1", m.String())
	}
	for _, m := range []domain.Month{domain.NewMonth(2025, time.November), domain.NewMonth(2027, time.November)} {
		f.reset()
		_, err = f.svc.Summary(ctx, "u1", m, domain.Date{})
		require.NoError(t, err)
		assert.Contains(t, *f.log, "version u1", "%s is within the window", m)
	}
}

func TestSummaryCacheFailsOpen(t *testing.T) {
	ctx := context.Background()
	down := errors.New("redis down")

	f := newFixture()
	f.cache.versionErr = down
	sum, err := f.svc.Summary(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, int64(3000), sum.Currencies[0].Expense)
	assert.NotContains(t, fmt.Sprint(*f.log), "set u1", "nothing cached without a version")
	assert.Equal(t, 1, f.metrics.counts["summary:error:version"])
	assert.Contains(t, f.logs.String(), `"msg":"dashboard cache failed"`)
	assert.Contains(t, f.logs.String(), `"level":"WARN"`)

	f = newFixture()
	f.cache.getErr = down
	_, err = f.svc.Summary(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, 1, f.metrics.counts["summary:error:get"])

	f = newFixture()
	f.cache.setErr = down
	_, err = f.svc.Summary(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, 1, f.metrics.counts["summary:error:set"])

	// An entry that cannot be read is a miss, computed again.
	f = newFixture()
	f.cache.entries["u1:v0:v1:2026-11:2026-11-08"] = []byte("{not json")
	sum, err = f.svc.Summary(ctx, "u1", domain.Month{}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, int64(3000), sum.Currencies[0].Expense)
	assert.Equal(t, 1, f.metrics.counts["summary:error:get"])
	assert.Equal(t, 1, f.metrics.counts["summary:miss"])
}

func TestSummaryErrors(t *testing.T) {
	ctx := context.Background()
	down := errors.New("mongo down")
	for name, broke := range map[string]func(f fixture){
		"accounts":    func(f fixture) { f.accounts.err = down },
		"flows":       func(f fixture) { f.ledger.flowErr = down },
		"legs":        func(f fixture) { f.ledger.legsErr = down },
		"recurrences": func(f fixture) { f.recurrences.err = down },
	} {
		f := newFixture()
		broke(f)
		_, err := f.svc.Summary(ctx, "u1", domain.Month{}, domain.Date{})
		require.ErrorIs(t, err, down, name)
		assert.NotContains(t, fmt.Sprint(*f.log), "set u1", name)
	}
}

func TestInvalidator(t *testing.T) {
	log := &calls{}
	c := newMemCache(log)
	metrics := &countingMetrics{counts: map[string]int{}}
	var logs bytes.Buffer
	inv := NewInvalidator(c, Options{Metrics: metrics, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})

	// It runs even when the request that wrote is gone.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inv.Invalidate(ctx, "u1")
	assert.Equal(t, 1, c.bumped)
	assert.Zero(t, c.bumpedWithCancelledCtx)
	assert.Equal(t, 1, c.versions["u1"])

	c.bumpErr = errors.New("redis down")
	inv.Invalidate(context.Background(), "u1")
	assert.Equal(t, 1, metrics.counts["summary:error:invalidate"])
	assert.Contains(t, logs.String(), `"msg":"invalidate dashboard summaries failed"`)

	NewInvalidator(c, Options{}).Invalidate(context.Background(), "u1")
}

func TestCodec(t *testing.T) {
	month := domain.NewMonth(2026, time.November)
	sum := domain.BuildSummary(month, day(t, "2026-11-08"),
		[]domain.Account{{ID: "bank", Currency: "BRL", InitialBalance: 100}, {ID: "usd", Currency: "USD"}},
		[]domain.FlowSum{
			{Month: month, Currency: "BRL", Kind: domain.TransactionExpense, CategoryID: "food", Amount: 30, Count: 2},
			{Month: month, Currency: "BRL", Kind: domain.TransactionIncome, CategoryID: "salary", Amount: 90, Count: 1},
		},
		[]domain.LegSum{{AccountID: "bank", Month: month, Sum: 60}},
		[]domain.Transaction{{Kind: domain.TransactionIncome, Currency: "USD", Amount: 7, Date: day(t, "2026-11-30")}})
	b, err := encode(sum)
	require.NoError(t, err)
	back, err := decode(b)
	require.NoError(t, err)
	assert.Equal(t, sum, back)

	// The history may reach before domain.MinMonth.
	early := domain.BuildSummary(domain.NewMonth(1900, time.March), day(t, "2026-11-08"), []domain.Account{{ID: "a", Currency: "BRL"}}, nil, nil, nil)
	b, err = encode(early)
	require.NoError(t, err)
	back, err = decode(b)
	require.NoError(t, err)
	assert.Equal(t, "1899-04", back.Currencies[0].History[0].Month.String())

	for name, raw := range map[string]string{
		"json":          "{",
		"month":         `{"month":"11/2026","asOf":"2026-11-08"}`,
		"day":           `{"month":"2026-11","asOf":"2026-11-31"}`,
		"history month": `{"month":"2026-11","asOf":"2026-11-08","currencies":[{"history":[{"month":"x"}]}]}`,
	} {
		_, err := decode([]byte(raw))
		assert.Error(t, err, name)
	}
}
