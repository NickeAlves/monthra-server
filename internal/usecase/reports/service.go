package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/NickeAlves/monthra-server/internal/audit"
	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Service serves the caller's reports.
type Service struct {
	accounts    Accounts
	categories  Categories
	ledger      Ledger
	recurrences Recurrences
	investments Investments
	auditor     Auditor
	metrics     Metrics
	now         func() time.Time
}

// Options tunes the Service.
type Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
	// Investments adds the income of the investments to the reports; it
	// defaults to none.
	Investments Investments
	// Auditor defaults to recording nothing, Metrics to counting nothing.
	Auditor Auditor
	Metrics Metrics
}

type noInvestments struct{}

func (noInvestments) IncomeFlows(context.Context, string, domain.Date, domain.Date) ([]domain.FlowSum, error) {
	return nil, nil
}

type noAuditor struct{}

func (noAuditor) Record(context.Context, audit.Entry) {}

type noMetrics struct{}

func (noMetrics) Exported(string) {}

func (o Options) withDefaults() Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Investments == nil {
		o.Investments = noInvestments{}
	}
	if o.Auditor == nil {
		o.Auditor = noAuditor{}
	}
	if o.Metrics == nil {
		o.Metrics = noMetrics{}
	}
	return o
}

// NewService returns the service.
func NewService(accounts Accounts, categories Categories, ledger Ledger, recurrences Recurrences, o Options) *Service {
	o = o.withDefaults()
	return &Service{
		accounts: accounts, categories: categories, ledger: ledger, recurrences: recurrences,
		investments: o.Investments, auditor: o.Auditor, metrics: o.Metrics, now: o.Now,
	}
}

// SummaryQuery asks for the report from month From through month To (zero
// ones take domain.ReportPeriod's defaults) for a reader whose today is
// AsOf (their local day, within a day of today in UTC: domain.Today).
type SummaryQuery struct {
	From, To domain.Month
	AsOf     domain.Date
}

// Summary returns the caller's report of the period q asks for, or a
// *domain.ValidationError for a period that is not one. The occurrences
// due are created first, so the report counts them as the dashboard does.
func (s *Service) Summary(ctx context.Context, userID string, q SummaryQuery) (domain.Report, error) {
	today := domain.Today(q.AsOf, s.now())
	period, err := domain.ReportPeriod(q.From, q.To, today)
	if err != nil {
		return domain.Report{}, err
	}
	s.recurrences.Materialize(ctx, userID, today)

	in := domain.ReportInput{Period: period, Today: today}
	// One read of each sum: the flows of both periods at once.
	before, from, to := period.Previous().From.Day(1), period.From.Day(1), period.To.LastDay()
	if in.Accounts, err = s.accounts.List(ctx, userID, true); err != nil {
		return domain.Report{}, fmt.Errorf("list accounts: %w", err)
	}
	if in.Flows, err = s.ledger.Flows(ctx, userID, before, to); err != nil {
		return domain.Report{}, fmt.Errorf("sum flows: %w", err)
	}
	if in.Income, err = s.investments.IncomeFlows(ctx, userID, before, to); err != nil {
		return domain.Report{}, fmt.Errorf("sum investment income: %w", err)
	}
	if in.ByAccount, err = s.ledger.AccountFlows(ctx, userID, from, to); err != nil {
		return domain.Report{}, fmt.Errorf("sum flows by account: %w", err)
	}
	if in.ByTag, err = s.ledger.TagFlows(ctx, userID, from, to); err != nil {
		return domain.Report{}, fmt.Errorf("sum flows by tag: %w", err)
	}
	if in.Legs, err = s.ledger.LegSums(ctx, userID, period.From, domain.SummaryCut(period.To, today)); err != nil {
		return domain.Report{}, fmt.Errorf("sum balances: %w", err)
	}
	return domain.BuildReport(in), nil
}
