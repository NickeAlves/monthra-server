package reports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/audit"
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

func month(t *testing.T, s string) domain.Month {
	t.Helper()
	m, err := domain.ParseMonth(s)
	require.NoError(t, err)
	return m
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

type fakeCategories struct {
	log  *calls
	list []domain.Category
	err  error
}

func (f *fakeCategories) List(_ context.Context, userID string, kind domain.CategoryKind) ([]domain.Category, error) {
	f.log.add("categories %s %q", userID, kind)
	return f.list, f.err
}

type fakeLedger struct {
	log       *calls
	flows     []domain.FlowSum
	byAccount []domain.AccountFlowSum
	byTag     []domain.TagFlowSum
	legs      []domain.LegSum
	// txs is what List pages through, in the list's order.
	txs []domain.Transaction
	// errs fails the named method.
	errs map[string]error
}

func (f *fakeLedger) Flows(_ context.Context, userID string, from, to domain.Date) ([]domain.FlowSum, error) {
	f.log.add("flows %s %s %s", userID, from, to)
	return f.flows, f.errs["flows"]
}

func (f *fakeLedger) AccountFlows(_ context.Context, userID string, from, to domain.Date) ([]domain.AccountFlowSum, error) {
	f.log.add("by account %s %s %s", userID, from, to)
	return f.byAccount, f.errs["by account"]
}

func (f *fakeLedger) TagFlows(_ context.Context, userID string, from, to domain.Date) ([]domain.TagFlowSum, error) {
	f.log.add("by tag %s %s %s", userID, from, to)
	return f.byTag, f.errs["by tag"]
}

func (f *fakeLedger) LegSums(_ context.Context, userID string, from domain.Month, through domain.Date) ([]domain.LegSum, error) {
	f.log.add("legs %s %s %s", userID, from, through)
	return f.legs, f.errs["legs"]
}

func (f *fakeLedger) List(_ context.Context, userID string, filter domain.TransactionFilter, after *domain.TransactionCursor, limit int) ([]domain.Transaction, error) {
	start := 0
	if after != nil {
		for i, t := range f.txs {
			if t.ID == after.ID {
				start = i + 1
			}
		}
		f.log.add("list %s %s after %s %d", userID, filter.Tag, after.ID, limit)
	} else {
		f.log.add("list %s %s %d", userID, filter.Tag, limit)
	}
	if err := f.errs["list"]; err != nil && start > 0 {
		return nil, err
	}
	end := min(start+limit, len(f.txs))
	return f.txs[start:end], nil
}

type fakeRecurrences struct{ log *calls }

func (f *fakeRecurrences) Materialize(_ context.Context, userID string, asOf domain.Date) {
	f.log.add("materialize %s %s", userID, asOf)
}

type fakeInvestments struct {
	log    *calls
	income []domain.FlowSum
	err    error
}

func (f *fakeInvestments) IncomeFlows(_ context.Context, userID string, from, to domain.Date) ([]domain.FlowSum, error) {
	f.log.add("income %s %s %s", userID, from, to)
	return f.income, f.err
}

type fakeAuditor struct{ entries []audit.Entry }

func (f *fakeAuditor) Record(_ context.Context, e audit.Entry) { f.entries = append(f.entries, e) }

type fakeMetrics struct{ exported []string }

func (f *fakeMetrics) Exported(dataset string) { f.exported = append(f.exported, dataset) }

type fixture struct {
	log         *calls
	accounts    *fakeAccounts
	categories  *fakeCategories
	ledger      *fakeLedger
	investments *fakeInvestments
	auditor     *fakeAuditor
	metrics     *fakeMetrics
	svc         *Service
}

func newFixture() *fixture {
	log := &calls{}
	f := &fixture{
		log:         log,
		accounts:    &fakeAccounts{log: log, list: []domain.Account{{ID: "bank", Name: "Bank", Currency: "BRL", InitialBalance: 1000}}},
		categories:  &fakeCategories{log: log, list: []domain.Category{{ID: "food", Kind: domain.CategoryExpense}}},
		ledger:      &fakeLedger{log: log, errs: map[string]error{}},
		investments: &fakeInvestments{log: log},
		auditor:     &fakeAuditor{},
		metrics:     &fakeMetrics{},
	}
	f.svc = NewService(f.accounts, f.categories, f.ledger, &fakeRecurrences{log: log}, Options{
		Now: func() time.Time { return t0 }, Investments: f.investments, Auditor: f.auditor, Metrics: f.metrics,
	})
	return f
}

func TestSummary(t *testing.T) {
	f := newFixture()
	f.ledger.flows = []domain.FlowSum{
		{Month: month(t, "2026-11"), Currency: "BRL", Kind: domain.TransactionExpense, CategoryID: "food", Amount: 300, Count: 1},
		{Month: month(t, "2026-08"), Currency: "BRL", Kind: domain.TransactionExpense, CategoryID: "food", Amount: 100, Count: 1},
	}
	f.investments.income = []domain.FlowSum{
		{Month: month(t, "2026-10"), Currency: "BRL", Kind: domain.TransactionIncome, CategoryID: "dividends", Amount: 50, Count: 1},
	}
	f.ledger.byAccount = []domain.AccountFlowSum{{Currency: "BRL", Kind: domain.TransactionExpense, AccountID: "bank", Amount: 300, Count: 1}}
	f.ledger.byTag = []domain.TagFlowSum{{Currency: "BRL", Kind: domain.TransactionExpense, Tag: "home", Amount: 300, Count: 1}}
	f.ledger.legs = []domain.LegSum{{AccountID: "bank", Month: month(t, "2026-11"), Sum: -300}}

	r, err := f.svc.Summary(context.Background(), "u1", SummaryQuery{From: month(t, "2026-09"), To: month(t, "2026-11"), AsOf: day(t, "2026-11-07")})
	require.NoError(t, err)
	assert.Equal(t, calls{
		"materialize u1 2026-11-07",
		"accounts u1 true",
		"flows u1 2026-06-01 2026-11-30",
		"income u1 2026-06-01 2026-11-30",
		"by account u1 2026-09-01 2026-11-30",
		"by tag u1 2026-09-01 2026-11-30",
		"legs u1 2026-09 2026-11-07",
	}, *f.log, "the occurrences first; both periods in one read of the flows; the balances up to the reader's today")
	assert.Equal(t, domain.Period{From: month(t, "2026-06"), To: month(t, "2026-08")}, r.Previous)
	require.Len(t, r.Currencies, 1)
	brl := r.Currencies[0]
	assert.Equal(t, int64(50), brl.Income)
	assert.Equal(t, int64(300), brl.Expense)
	assert.Equal(t, int64(100), brl.Previous.Expense)
	assert.Equal(t, int64(50), brl.InvestmentIncome)
	require.NotNil(t, brl.Months[2].NetWorth)
	assert.Equal(t, int64(700), *brl.Months[2].NetWorth)
	assert.Empty(t, f.auditor.entries, "a report is no export")
}

func TestSummaryDefaultsAndValidation(t *testing.T) {
	f := newFixture()
	r, err := f.svc.Summary(context.Background(), "u1", SummaryQuery{})
	require.NoError(t, err)
	assert.Equal(t, domain.Period{From: month(t, "2025-12"), To: month(t, "2026-11")}, r.Period, "the 12 months up to UTC today's")
	assert.Equal(t, "2026-11-08", r.AsOf.String())

	f = newFixture()
	_, err = f.svc.Summary(context.Background(), "u1", SummaryQuery{From: month(t, "2026-11"), To: month(t, "2026-10")})
	var ve *domain.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, []domain.FieldError{{Field: "to", Code: domain.CodeInvalid}}, ve.Fields)
	assert.Empty(t, *f.log, "an invalid period reads nothing")
}

func TestSummaryFailures(t *testing.T) {
	boom := errors.New("boom")
	for _, name := range []string{"accounts", "flows", "income", "by account", "by tag", "legs"} {
		f := newFixture()
		switch name {
		case "accounts":
			f.accounts.err = boom
		case "income":
			f.investments.err = boom
		default:
			f.ledger.errs[name] = boom
		}
		_, err := f.svc.Summary(context.Background(), "u1", SummaryQuery{})
		assert.ErrorIs(t, err, boom, name)
	}
}

func TestSummaryWithoutInvestments(t *testing.T) {
	log := &calls{}
	svc := NewService(&fakeAccounts{log: log}, &fakeCategories{log: log}, &fakeLedger{log: log}, &fakeRecurrences{log: log}, Options{})
	r, err := svc.Summary(context.Background(), "u1", SummaryQuery{})
	require.NoError(t, err)
	assert.Empty(t, r.Currencies)
	assert.Zero(t, countPrefix(*log, "income"), "no investments: no income read")
}

// pagedTransactions returns n transactions in the list's order.
func pagedTransactions(n int) []domain.Transaction {
	out := make([]domain.Transaction, n)
	for i := range out {
		out[i] = domain.Transaction{ID: "t" + strconv.Itoa(i), Kind: domain.TransactionExpense, AccountID: "bank", CategoryID: "food", Amount: 1}
	}
	return out
}

func TestExportTransactions(t *testing.T) {
	f := newFixture()
	f.ledger.txs = pagedTransactions(ExportPageSize + 2)
	r := Requester{UserID: "u1", SessionID: "s1", IP: "203.0.113.7", UserAgent: "curl"}

	exp, err := f.svc.ExportTransactions(context.Background(), r, ExportQuery{Filter: domain.TransactionFilter{Tag: "home"}, AsOf: day(t, "2026-11-07")})
	require.NoError(t, err)
	assert.Equal(t, "Bank", exp.Accounts["bank"].Name)
	assert.Contains(t, exp.Categories, "food")
	assert.Equal(t, []audit.Entry{{
		Event: audit.EventTransactionsExported, UserID: "u1", SessionID: "s1", IP: "203.0.113.7", UserAgent: "curl", Reason: "csv",
	}}, f.auditor.entries)
	assert.Equal(t, []string{"transactions"}, f.metrics.exported)

	var ids []string
	for tx, err := range exp.Rows {
		require.NoError(t, err)
		ids = append(ids, tx.ID)
	}
	assert.Len(t, ids, ExportPageSize+2)
	assert.Equal(t, "t501", ids[len(ids)-1])
	assert.Equal(t, calls{
		"materialize u1 2026-11-07",
		"accounts u1 true",
		`categories u1 ""`,
		"list u1 home 500",
		"list u1 home after t499 500",
	}, *f.log, "pages of the list, the next one after the last read; a short page ends")
}

func TestExportTransactionsStopsEarly(t *testing.T) {
	f := newFixture()
	f.ledger.txs = pagedTransactions(ExportPageSize * 3)
	exp, err := f.svc.ExportTransactions(context.Background(), Requester{UserID: "u1"}, ExportQuery{})
	require.NoError(t, err)
	n := 0
	for _, err := range exp.Rows {
		require.NoError(t, err)
		n++
		if n == 3 {
			break
		}
	}
	assert.Equal(t, 3, n)
	assert.Equal(t, 1, countPrefix(*f.log, "list "), "a reader that stops reads no more pages")
}

func TestExportTransactionsFailures(t *testing.T) {
	boom := errors.New("boom")
	f := newFixture()
	f.accounts.err = boom
	_, err := f.svc.ExportTransactions(context.Background(), Requester{UserID: "u1"}, ExportQuery{})
	require.ErrorIs(t, err, boom)
	assert.Empty(t, f.auditor.entries, "nothing exported, nothing recorded")

	f = newFixture()
	f.categories.err = boom
	_, err = f.svc.ExportTransactions(context.Background(), Requester{UserID: "u1"}, ExportQuery{})
	require.ErrorIs(t, err, boom)

	// A page that fails after the first yields the error and ends.
	f = newFixture()
	f.ledger.txs = pagedTransactions(ExportPageSize + 10)
	f.ledger.errs["list"] = boom
	exp, err := f.svc.ExportTransactions(context.Background(), Requester{UserID: "u1"}, ExportQuery{})
	require.NoError(t, err)
	var n int
	var last error
	for _, err := range exp.Rows {
		if err != nil {
			last = err
			continue
		}
		n++
	}
	assert.Equal(t, ExportPageSize, n)
	assert.ErrorIs(t, last, boom)
}

func countPrefix(list []string, prefix string) int {
	n := 0
	for _, s := range list {
		if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}
