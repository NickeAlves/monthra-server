package dashboard

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

const (
	// cacheName labels the metrics of the summaries cache.
	cacheName = "summary"
	// CacheWindow is how many months away from the reader's month a summary
	// is still cached: the months people page through. Farther ones are
	// computed on each read, which bounds the entries one user can fill.
	CacheWindow = 12
	// invalidateTimeout bounds the bump after a write, which runs even when
	// the request that wrote is gone.
	invalidateTimeout = 2 * time.Second
)

// Service serves the caller's dashboard.
type Service struct {
	accounts    Accounts
	ledger      Ledger
	recurrences Recurrences
	cache       Cache
	metrics     Metrics
	logger      *slog.Logger
	now         func() time.Time
}

// Options tunes the Service and the Invalidator.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
	// Logger records the cache failures; it defaults to none.
	Logger *slog.Logger
	// Metrics defaults to counting nothing.
	Metrics Metrics
}

type noMetrics struct{}

func (noMetrics) CacheRequest(string, string) {}
func (noMetrics) CacheError(string, string)   {}

func (o Options) withDefaults() Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Metrics == nil {
		o.Metrics = noMetrics{}
	}
	return o
}

// NewService returns the service.
func NewService(accounts Accounts, ledger Ledger, recurrences Recurrences, cache Cache, o Options) *Service {
	o = o.withDefaults()
	return &Service{
		accounts: accounts, ledger: ledger, recurrences: recurrences, cache: cache,
		metrics: o.Metrics, logger: o.Logger, now: o.Now,
	}
}

// Summary returns the caller's dashboard of month (zero is the reader's
// month) for a reader whose today is asOf (their local day, within a day of
// today in UTC: domain.Today). The occurrences due are created first; then
// the summary comes from the cache when it holds it under the current
// version, or from the transactions, and is cached. The cache fails open:
// a Redis failure is logged and the summary computed.
func (s *Service) Summary(ctx context.Context, userID string, month domain.Month, asOf domain.Date) (domain.Summary, error) {
	today := domain.Today(asOf, s.now())
	if month.IsZero() {
		month = domain.MonthOf(today)
	}
	// Creating occurrences bumps the version, so the cache is read after.
	s.recurrences.Materialize(ctx, userID, today)
	cut := domain.SummaryCut(month, today)
	slot := codecVersion + ":" + month.String() + ":" + cut.String()

	var version string
	if away := month.Sub(domain.MonthOf(today)); -CacheWindow <= away && away <= CacheWindow {
		version = s.version(ctx, userID)
	}
	if version != "" {
		if sum, ok := s.cached(ctx, userID, version, slot); ok {
			return sum, nil
		}
	}
	sum, err := s.build(ctx, userID, month, today, cut)
	if err != nil {
		return domain.Summary{}, err
	}
	if version != "" {
		s.store(ctx, userID, version, slot, sum)
	}
	return sum, nil
}

// build computes the summary from the accounts, the transactions and the
// recurrences.
func (s *Service) build(ctx context.Context, userID string, month domain.Month, today, cut domain.Date) (domain.Summary, error) {
	first := month.AddMonths(1 - domain.SummaryHistoryMonths)
	accounts, err := s.accounts.List(ctx, userID, true)
	if err != nil {
		return domain.Summary{}, fmt.Errorf("list accounts: %w", err)
	}
	flows, err := s.ledger.Flows(ctx, userID, first.Day(1), month.LastDay())
	if err != nil {
		return domain.Summary{}, fmt.Errorf("sum flows: %w", err)
	}
	legs, err := s.ledger.LegSums(ctx, userID, first, cut)
	if err != nil {
		return domain.Summary{}, fmt.Errorf("sum balances: %w", err)
	}
	scheduled, err := s.recurrences.Scheduled(ctx, userID, month.Day(1), month.LastDay())
	if err != nil {
		return domain.Summary{}, fmt.Errorf("project recurrences: %w", err)
	}
	return domain.BuildSummary(month, today, accounts, flows, legs, scheduled), nil
}

// version returns the version of the caller's summaries, or "" when the
// cache cannot tell (the summary is then computed and not cached).
func (s *Service) version(ctx context.Context, userID string) string {
	v, err := s.cache.Version(ctx, userID)
	if err != nil {
		s.cacheFailed(ctx, "version", err)
		return ""
	}
	return v
}

// cached returns the summary at slot, if the cache holds a readable one.
func (s *Service) cached(ctx context.Context, userID, version, slot string) (domain.Summary, bool) {
	b, ok, err := s.cache.Get(ctx, userID, version, slot)
	if err != nil {
		s.cacheFailed(ctx, "get", err)
		return domain.Summary{}, false
	}
	if ok {
		sum, err := decode(b)
		if err == nil {
			s.metrics.CacheRequest(cacheName, "hit")
			return sum, true
		}
		s.cacheFailed(ctx, "get", err)
	}
	s.metrics.CacheRequest(cacheName, "miss")
	return domain.Summary{}, false
}

// store caches sum at slot.
func (s *Service) store(ctx context.Context, userID, version, slot string, sum domain.Summary) {
	b, err := encode(sum)
	if err == nil {
		err = s.cache.Set(ctx, userID, version, slot, b)
	}
	if err != nil {
		s.cacheFailed(ctx, "set", err)
	}
}

func (s *Service) cacheFailed(ctx context.Context, op string, err error) {
	s.metrics.CacheError(cacheName, op)
	s.logger.WarnContext(ctx, "dashboard cache failed", "op", op, "error", err)
}

// Invalidator drops the cached summaries of a user: the use cases call it
// after every write to the user's finance data (one write may change every
// month after it, through the net worth). It bumps the version of the
// user's summaries, never fails (a failure is logged; the summaries cached
// then live out their TTL) and runs even when the request is gone.
type Invalidator struct {
	cache   Cache
	metrics Metrics
	logger  *slog.Logger
}

// NewInvalidator returns the invalidator of cache. It is built before the
// use cases that write (the recurrences among them), and the Service after.
func NewInvalidator(cache Cache, o Options) *Invalidator {
	o = o.withDefaults()
	return &Invalidator{cache: cache, metrics: o.Metrics, logger: o.Logger}
}

// Invalidate drops the cached summaries of userID.
func (i *Invalidator) Invalidate(ctx context.Context, userID string) {
	bumpCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invalidateTimeout)
	defer cancel()
	if err := i.cache.Bump(bumpCtx, userID); err != nil {
		i.metrics.CacheError(cacheName, "invalidate")
		i.logger.WarnContext(ctx, "invalidate dashboard summaries failed", "error", err)
	}
}
