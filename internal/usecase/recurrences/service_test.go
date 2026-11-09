package recurrences

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// t0 is 2026-11-08 in UTC.
var t0 = time.Date(2026, 11, 8, 23, 0, 0, 0, time.UTC)

// memStore keeps recurrences by id, in insertion order.
type memStore struct {
	recs     map[string]domain.Recurrence
	order    []string
	next     int
	err      error
	dueErr   error
	advances []string
}

func newMemStore() *memStore { return &memStore{recs: map[string]domain.Recurrence{}} }

func (m *memStore) List(_ context.Context, userID string) ([]domain.Recurrence, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := []domain.Recurrence{}
	for _, id := range m.order {
		if r, ok := m.recs[id]; ok && r.UserID == userID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) Due(_ context.Context, userID string, through domain.Date, limit int) ([]domain.Recurrence, error) {
	if m.dueErr != nil {
		return nil, m.dueErr
	}
	var out []domain.Recurrence
	for _, id := range m.order {
		r, ok := m.recs[id]
		if ok && r.UserID == userID && r.Active() && !through.Before(r.Next) {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b domain.Recurrence) int { return a.Next.Time().Compare(b.Next.Time()) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) Count(_ context.Context, userID string) (int, error) {
	list, err := m.List(context.Background(), userID)
	return len(list), err
}

func (m *memStore) Insert(_ context.Context, r domain.Recurrence) (domain.Recurrence, error) {
	if m.err != nil {
		return domain.Recurrence{}, m.err
	}
	m.next++
	r.ID = fmt.Sprintf("r%d", m.next)
	m.recs[r.ID] = r
	m.order = append(m.order, r.ID)
	return r, nil
}

func (m *memStore) ByID(_ context.Context, userID, id string) (domain.Recurrence, error) {
	if m.err != nil {
		return domain.Recurrence{}, m.err
	}
	r, ok := m.recs[id]
	if !ok || r.UserID != userID {
		return domain.Recurrence{}, domain.ErrNotFound
	}
	return r, nil
}

func (m *memStore) Update(ctx context.Context, r domain.Recurrence) error {
	current, err := m.ByID(ctx, r.UserID, r.ID)
	if err != nil {
		return err
	}
	r.Last = current.Last // Advance moves it, Update never does
	m.recs[r.ID] = r
	return nil
}

func (m *memStore) Advance(_ context.Context, userID, id string, from, next, last domain.Date) error {
	if m.err != nil {
		return m.err
	}
	r, ok := m.recs[id]
	if !ok || r.UserID != userID || !r.Next.Equal(from) {
		m.advances = append(m.advances, id+":lost")
		return nil
	}
	r.Next, r.Last = next, last
	m.recs[id] = r
	m.advances = append(m.advances, id+":"+last.String())
	return nil
}

func (m *memStore) Delete(ctx context.Context, userID, id string) error {
	if _, err := m.ByID(ctx, userID, id); err != nil {
		return err
	}
	delete(m.recs, id)
	return nil
}

func (m *memStore) AccountUsage(_ context.Context, userID, id string) (bool, bool, error) {
	if m.err != nil {
		return false, false, m.err
	}
	var used, active bool
	for _, r := range m.recs {
		if r.UserID == userID && (r.AccountID == id || r.ToAccountID == id) {
			used, active = true, active || r.Active()
		}
	}
	return used, active, nil
}

// memLedger stores occurrences once per recurrence and rule day, as the
// unique index does.
type memLedger struct {
	stored     []domain.Transaction
	err        error
	earlierErr error
}

func (l *memLedger) HasOccurrencesBefore(_ context.Context, _, recurrenceID string, day domain.Date) (bool, error) {
	if l.earlierErr != nil {
		return false, l.earlierErr
	}
	return slices.ContainsFunc(l.stored, func(o domain.Transaction) bool {
		return o.Recurrence.ID == recurrenceID && o.Recurrence.Date.Before(day)
	}), nil
}

func (l *memLedger) InsertOccurrences(_ context.Context, ts []domain.Transaction) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	n := 0
	for _, t := range ts {
		if !slices.ContainsFunc(l.stored, func(o domain.Transaction) bool {
			return o.Recurrence.ID == t.Recurrence.ID && o.Recurrence.Date.Equal(t.Recurrence.Date)
		}) {
			l.stored = append(l.stored, t)
			n++
		}
	}
	return n, nil
}

func (l *memLedger) days(recurrenceID string) []string {
	var out []string
	for _, t := range l.stored {
		if t.Recurrence.ID == recurrenceID {
			out = append(out, t.Date.String())
		}
	}
	return out
}

// refs finds the accounts and categories of u1.
type refs struct {
	accounts   map[string]domain.Account
	categories map[string]domain.Category
	lookups    []string
	err        error
}

func (r *refs) ByID(_ context.Context, userID, id string) (domain.Account, error) {
	r.lookups = append(r.lookups, "account:"+id)
	if r.err != nil {
		return domain.Account{}, r.err
	}
	a, ok := r.accounts[id]
	if !ok || a.UserID != userID {
		return domain.Account{}, domain.ErrNotFound
	}
	return a, nil
}

func (r *refs) List(_ context.Context, userID string, includeArchived bool) ([]domain.Account, error) {
	r.lookups = append(r.lookups, "accounts")
	if r.err != nil {
		return nil, r.err
	}
	var out []domain.Account
	for _, id := range []string{"bank", "card", "old", "savings", "bobs"} {
		if a, ok := r.accounts[id]; ok && a.UserID == userID && (includeArchived || !a.Archived()) {
			out = append(out, a)
		}
	}
	return out, nil
}

type categoryFinder struct{ *refs }

func (c categoryFinder) ByID(_ context.Context, userID, id string) (domain.Category, error) {
	c.lookups = append(c.lookups, "category:"+id)
	cat, ok := c.categories[id]
	if !ok || cat.UserID != userID {
		return domain.Category{}, domain.ErrNotFound
	}
	return cat, nil
}

type countingMetrics struct{ created []string }

func (m *countingMetrics) TransactionCreated(kind string) { m.created = append(m.created, kind) }

// countingHook counts the hook calls of each user.
type countingHook map[string]int

func (c countingHook) AfterWrite(_ context.Context, userID string) { c[userID]++ }

type fixture struct {
	svc     *Service
	store   *memStore
	ledger  *memLedger
	refs    *refs
	metrics *countingMetrics
	logs    *bytes.Buffer
	invalid countingHook
}

func newFixture() fixture {
	archived := t0.Add(-time.Hour)
	r := &refs{
		accounts: map[string]domain.Account{
			"bank":    {ID: "bank", UserID: "u1", Currency: "BRL"},
			"savings": {ID: "savings", UserID: "u1", Currency: "BRL"},
			"old":     {ID: "old", UserID: "u1", Currency: "BRL", ArchivedAt: &archived},
			"bobs":    {ID: "bobs", UserID: "u2", Currency: "BRL"},
			"card": {
				ID: "card", UserID: "u1", Type: domain.AccountCreditCard, Currency: "BRL",
				Card: &domain.CardSettings{ClosingDay: 25, DueDay: 5},
			},
		},
		categories: map[string]domain.Category{
			"rent":   {ID: "rent", UserID: "u1", Kind: domain.CategoryExpense},
			"home":   {ID: "home", UserID: "u1", Kind: domain.CategoryExpense},
			"salary": {ID: "salary", UserID: "u1", Kind: domain.CategoryIncome},
		},
	}
	store, ledger, metrics, logs, invalid := newMemStore(), &memLedger{}, &countingMetrics{}, &bytes.Buffer{}, countingHook{}
	svc := NewService(store, ledger, r, categoryFinder{r}, Options{
		Now: func() time.Time { return t0 }, Metrics: metrics, Logger: slog.New(slog.NewTextHandler(logs, nil)),
		AfterWrite: invalid,
	})
	return fixture{svc: svc, store: store, ledger: ledger, refs: r, metrics: metrics, logs: logs, invalid: invalid}
}

func rent(start string) domain.RecurrenceInput {
	return domain.RecurrenceInput{
		Kind: "expense", AccountID: "bank", CategoryID: "rent", Amount: 250000, Description: "Rent",
		Frequency: "monthly", StartDate: start,
	}
}

func day(t *testing.T, s string) domain.Date {
	t.Helper()
	d, err := domain.ParseDate(s)
	require.NoError(t, err)
	return d
}

func fields(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *domain.ValidationError
	require.ErrorAs(t, err, &ve)
	out := map[string]string{}
	for _, f := range ve.Fields {
		out[f.Field] = f.Code
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func TestCreateMaterializesTheHistory(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	r, err := f.svc.Create(ctx, "u1", rent("2026-08-05"), domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-08-05", "2026-09-05", "2026-10-05", "2026-11-05"}, f.ledger.days(r.ID))
	assert.Equal(t, "2026-12-05", r.Next.String(), "answered after its occurrences")
	assert.Equal(t, "2026-11-05", r.Last.String())
	assert.Equal(t, []string{"expense", "expense", "expense", "expense"}, f.metrics.created)
	assert.Equal(t, []string{"account:bank", "category:rent", "accounts"}, f.refs.lookups)

	// Reading again creates nothing new.
	_, err = f.svc.List(ctx, "u1", domain.Date{})
	require.NoError(t, err)
	assert.Len(t, f.ledger.stored, 4)

	future, err := f.svc.Create(ctx, "u1", rent("2027-01-10"), domain.Date{})
	require.NoError(t, err)
	assert.Empty(t, f.ledger.days(future.ID))
	assert.Equal(t, "2027-01-10", future.Next.String())

	_, err = f.svc.Create(ctx, "u1", domain.RecurrenceInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "savings", Amount: 50000, Frequency: "weekly", StartDate: "2026-11-02",
	}, domain.Date{})
	require.NoError(t, err)

	_, err = f.svc.Create(ctx, "u1", domain.RecurrenceInput{Kind: "expense", AccountID: "bobs", CategoryID: "rent", Amount: 1, Frequency: "monthly", StartDate: "2026-11-01"}, domain.Date{})
	assert.Equal(t, map[string]string{"accountId": domain.CodeInvalid}, fields(t, err), "another user's account reads like none")
	_, err = f.svc.Create(ctx, "u1", domain.RecurrenceInput{Kind: "expense", AccountID: "old", CategoryID: "rent", Amount: 1, Frequency: "monthly", StartDate: "2026-11-01"}, domain.Date{})
	assert.Equal(t, map[string]string{"accountId": domain.CodeArchived}, fields(t, err))
}

func TestCreateLimitsAndFailures(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.svc.max = 1
	_, err := f.svc.Create(ctx, "u1", rent("2027-01-10"), domain.Date{})
	require.NoError(t, err)
	_, err = f.svc.Create(ctx, "u1", rent("2027-01-10"), domain.Date{})
	assert.ErrorIs(t, err, domain.ErrLimitReached)
	_, err = f.svc.Create(ctx, "u2", domain.RecurrenceInput{Kind: "expense", AccountID: "bobs", CategoryID: "x", Amount: 1, Frequency: "monthly", StartDate: "2027-01-10"}, domain.Date{})
	assert.Equal(t, map[string]string{"categoryId": domain.CodeInvalid}, fields(t, err), "the cap is per user; this one fails on its category")

	f.refs.err = errors.New("down")
	_, err = f.svc.Create(ctx, "u1", rent("2027-01-10"), domain.Date{})
	assert.ErrorContains(t, err, "find account")
	f.refs.err = nil
	f.store.err = errors.New("down")
	f.svc.max = 10
	_, err = f.svc.Create(ctx, "u1", rent("2027-01-10"), domain.Date{})
	assert.ErrorContains(t, err, "count recurrences")
}

func TestMaterialize(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	r, err := f.svc.Create(ctx, "u1", rent("2027-01-10"), domain.Date{})
	require.NoError(t, err)

	// Starting today: the horizon is the reader's today, within a day of
	// today in UTC.
	today := domain.DateOf(t0)
	stored := f.store.recs[r.ID]
	stored.Rule.Start, stored.Rule.Frequency, stored.Next = today, domain.FrequencyDaily, today
	f.store.recs[r.ID] = stored
	f.svc.Materialize(ctx, "u1", day(t, "2030-01-01"))
	assert.Equal(t, []string{"2026-11-08", "2026-11-09"}, f.ledger.days(r.ID), "never past today + 1")
	f.svc.Materialize(ctx, "u1", day(t, "2026-01-01"))
	assert.Len(t, f.ledger.stored, 2, "an old asOf creates nothing more")
	assert.Equal(t, "2026-11-10", f.store.recs[r.ID].Next.String())
	assert.Equal(t, "2026-11-09", f.store.recs[r.ID].Last.String())

	// A pass whose recurrence did not advance (it lost the compare-and-set,
	// or failed) stores the same occurrences again, and they are skipped.
	stored = f.store.recs[r.ID]
	stored.Next = today
	f.store.recs[r.ID] = stored
	f.svc.Materialize(ctx, "u1", today.AddDays(1))
	assert.Len(t, f.ledger.stored, 2)
	assert.Equal(t, "2026-11-10", f.store.recs[r.ID].Next.String())
	assert.Len(t, f.metrics.created, 2, "only the new ones count")
}

func TestMaterializeIsBoundedPerPass(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	in := rent("2024-01-01")
	in.Frequency = "daily"
	r, err := f.svc.Create(ctx, "u1", in, domain.Date{})
	require.NoError(t, err)
	assert.Len(t, f.ledger.stored, MaxOccurrencesPerPass)
	assert.Equal(t, day(t, "2024-01-01").AddDays(MaxOccurrencesPerPass).String(), r.Next.String(), "the rest comes later")

	other, err := f.svc.Create(ctx, "u1", rent("2026-10-01"), domain.Date{})
	require.NoError(t, err)
	assert.Len(t, f.ledger.stored, 2*MaxOccurrencesPerPass, "the earliest due first")
	assert.Empty(t, f.ledger.days(other.ID))
	for range 3 {
		f.svc.Materialize(ctx, "u1", domain.Date{})
	}
	assert.Equal(t, "2026-11-08", f.store.recs[r.ID].Last.String())
	assert.Equal(t, []string{"2026-10-01", "2026-11-01"}, f.ledger.days(other.ID))
}

func TestMaterializeFailsOpen(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	r, err := f.svc.Create(ctx, "u1", rent("2027-01-10"), domain.Date{})
	require.NoError(t, err)
	stored := f.store.recs[r.ID]
	stored.Next, stored.Rule.Start = day(t, "2026-11-05"), day(t, "2026-11-05")
	f.store.recs[r.ID] = stored

	f.store.dueErr = errors.New("mongo down")
	list, err := f.svc.List(ctx, "u1", domain.Date{})
	require.NoError(t, err, "the read goes on")
	assert.Len(t, list, 1)
	assert.Contains(t, f.logs.String(), "materialize recurrences failed")
	f.store.dueErr = nil

	f.ledger.err = errors.New("mongo down")
	f.svc.Materialize(ctx, "u1", domain.Date{})
	assert.Contains(t, f.logs.String(), "create occurrences")
	assert.Equal(t, "2026-11-05", f.store.recs[r.ID].Next.String(), "not advanced past what was not stored")
	f.ledger.err = nil

	f.refs.err = errors.New("mongo down")
	f.svc.Materialize(ctx, "u1", domain.Date{})
	assert.Contains(t, f.logs.String(), "list accounts")
	f.refs.err = nil

	// An account deleted in a race leaves its recurrence due.
	delete(f.refs.accounts, "bank")
	f.svc.Materialize(ctx, "u1", domain.Date{})
	assert.Contains(t, f.logs.String(), "recurrence on a missing account")
	assert.Empty(t, f.ledger.stored)
}

func TestGetListDelete(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	r, err := f.svc.Create(ctx, "u1", rent("2027-01-10"), domain.Date{})
	require.NoError(t, err)

	got, err := f.svc.Get(ctx, "u1", r.ID, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, r, got)
	_, err = f.svc.Get(ctx, "u2", r.ID, domain.Date{})
	assert.ErrorIs(t, err, domain.ErrNotFound)
	theirs, err := f.svc.List(ctx, "u2", domain.Date{})
	require.NoError(t, err)
	assert.Empty(t, theirs)

	assert.ErrorIs(t, f.svc.Delete(ctx, "u2", r.ID), domain.ErrNotFound)
	require.NoError(t, f.svc.Delete(ctx, "u1", r.ID))
	assert.ErrorIs(t, f.svc.Delete(ctx, "u1", r.ID), domain.ErrNotFound)

	f.store.err = errors.New("down")
	_, err = f.svc.List(ctx, "u1", domain.Date{})
	assert.ErrorContains(t, err, "list recurrences")
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	r, err := f.svc.Create(ctx, "u1", rent("2026-08-05"), domain.Date{})
	require.NoError(t, err)

	f.refs.lookups = nil
	got, err := f.svc.Update(ctx, "u1", r.ID, domain.RecurrencePatch{Amount: ptr(int64(270000)), CategoryID: ptr("home")}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, int64(270000), got.Amount)
	assert.Equal(t, "home", got.CategoryID)
	assert.Equal(t, []string{"category:home"}, f.refs.lookups, "only what the patch moves to")
	for _, o := range f.ledger.stored {
		assert.Equal(t, int64(250000), o.Amount, "the transactions created stay")
	}

	same, err := f.svc.Update(ctx, "u1", r.ID, domain.RecurrencePatch{Amount: ptr(int64(270000))}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, got, same)

	// The rent moves to the 10th from December on.
	got, err = f.svc.Update(ctx, "u1", r.ID, domain.RecurrencePatch{StartDate: ptr("2026-12-10")}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, "2026-12-10", got.Next.String())
	_, err = f.svc.Update(ctx, "u1", r.ID, domain.RecurrencePatch{StartDate: ptr("2026-11-01")}, domain.Date{})
	assert.Equal(t, map[string]string{"startDate": domain.CodeOutOfRange}, fields(t, err))

	// Weekly from today on creates today's occurrence right away.
	got, err = f.svc.Update(ctx, "u1", r.ID, domain.RecurrencePatch{StartDate: ptr("2026-11-08"), Frequency: ptr("weekly")}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, "2026-11-08", got.Last.String())
	assert.Equal(t, "2026-11-15", got.Next.String())
	assert.Equal(t, []string{"2026-08-05", "2026-09-05", "2026-10-05", "2026-11-05", "2026-11-08"}, f.ledger.days(r.ID))

	ended, err := f.svc.Update(ctx, "u1", r.ID, domain.RecurrencePatch{EndDate: ptr("2026-11-10")}, domain.Date{})
	require.NoError(t, err)
	assert.False(t, ended.Active())
	f.refs.lookups = nil
	back, err := f.svc.Update(ctx, "u1", r.ID, domain.RecurrencePatch{EndDate: ptr("")}, domain.Date{})
	require.NoError(t, err)
	assert.True(t, back.Active())
	assert.Contains(t, f.refs.lookups, "account:bank", "its account is checked when it comes back")

	_, err = f.svc.Update(ctx, "u2", r.ID, domain.RecurrencePatch{Amount: ptr(int64(1))}, domain.Date{})
	assert.ErrorIs(t, err, domain.ErrNotFound)
	_, err = f.svc.Update(ctx, "u1", r.ID, domain.RecurrencePatch{AccountID: ptr("old")}, domain.Date{})
	assert.Equal(t, map[string]string{"accountId": domain.CodeArchived}, fields(t, err))
}

func TestUpdateRuleMaterializes(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	r, err := f.svc.Create(ctx, "u1", rent("2027-01-10"), domain.Date{})
	require.NoError(t, err)
	got, err := f.svc.Update(ctx, "u1", r.ID, domain.RecurrencePatch{StartDate: ptr("2026-10-10")}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-10-10"}, f.ledger.days(r.ID), "the new rule's due occurrences")
	assert.Equal(t, "2026-11-10", got.Next.String())
}

func TestUpcoming(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	card, err := f.svc.Create(ctx, "u1", domain.RecurrenceInput{
		Kind: "expense", AccountID: "card", CategoryID: "rent", Amount: 3990, Description: "Streaming",
		Frequency: "monthly", StartDate: "2026-10-24",
	}, domain.Date{})
	require.NoError(t, err)
	weekly, err := f.svc.Create(ctx, "u1", domain.RecurrenceInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "savings", Amount: 50000, Frequency: "weekly", StartDate: "2026-11-02",
	}, domain.Date{})
	require.NoError(t, err)
	ended, err := f.svc.Create(ctx, "u1", rent("2026-08-05"), domain.Date{})
	require.NoError(t, err)
	_, err = f.svc.Update(ctx, "u1", ended.ID, domain.RecurrencePatch{EndDate: ptr("2026-11-05")}, domain.Date{})
	require.NoError(t, err)

	list, err := f.svc.Upcoming(ctx, "u1", domain.Date{}, domain.Date{})
	require.NoError(t, err)
	var got []string
	for _, o := range list {
		got = append(got, o.Date.String()+" "+o.Recurrence.ID+" "+o.InvoiceMonth.String())
	}
	assert.Equal(t, []string{
		"2026-11-09 " + weekly.ID + " ",
		"2026-11-16 " + weekly.ID + " ",
		"2026-11-23 " + weekly.ID + " ",
		"2026-11-24 " + card.ID + " 2026-12",
		"2026-11-30 " + weekly.ID + " ",
		"2026-12-07 " + weekly.ID + " ",
	}, got, "after today, through 30 days, the ended one left out")

	list, err = f.svc.Upcoming(ctx, "u1", day(t, "2027-11-09"), domain.Date{})
	require.NoError(t, err)
	assert.Len(t, list, 12+53, "a year ahead")

	_, err = f.svc.Upcoming(ctx, "u1", day(t, "2026-11-07"), domain.Date{})
	assert.Equal(t, map[string]string{"to": domain.CodeInvalid}, fields(t, err))
	_, err = f.svc.Upcoming(ctx, "u1", day(t, "2027-11-10"), domain.Date{})
	assert.Equal(t, map[string]string{"to": domain.CodeOutOfRange}, fields(t, err))

	empty, err := f.svc.Upcoming(ctx, "u2", domain.Date{}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, []domain.Transaction{}, empty, "never null")

	daily := rent("2026-11-09")
	daily.Frequency = "daily"
	for range 3 {
		_, err = f.svc.Create(ctx, "u1", daily, domain.Date{})
		require.NoError(t, err)
	}
	list, err = f.svc.Upcoming(ctx, "u1", day(t, "2027-11-09"), domain.Date{})
	require.NoError(t, err)
	assert.Len(t, list, MaxUpcoming)
}

func TestSeries(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	r, err := f.svc.Create(ctx, "u1", rent("2026-08-05"), domain.Date{})
	require.NoError(t, err)
	occurrence := f.ledger.stored[2]
	occurrence.UserID = "u1"

	after := occurrence
	after.Amount = 270000
	require.NoError(t, f.svc.Follow(ctx, occurrence, after))
	assert.Equal(t, int64(270000), f.store.recs[r.ID].Amount)
	require.NoError(t, f.svc.Follow(ctx, occurrence, occurrence), "nothing to follow")
	require.NoError(t, f.svc.Follow(ctx, domain.Transaction{}, domain.Transaction{}), "not an occurrence")

	require.NoError(t, f.svc.EndBefore(ctx, occurrence))
	assert.Equal(t, "2026-10-04", f.store.recs[r.ID].Rule.End.String())
	assert.False(t, f.store.recs[r.ID].Active())

	first := f.ledger.stored[0]
	first.UserID = "u1"
	require.NoError(t, f.svc.EndBefore(ctx, first))
	assert.NotContains(t, f.store.recs, r.ID, "nothing left before the first one")
	require.NoError(t, f.svc.EndBefore(ctx, first), "already gone")
	require.NoError(t, f.svc.Follow(ctx, occurrence, after), "already gone")
	require.NoError(t, f.svc.Remove(ctx, first), "already gone")
	require.NoError(t, f.svc.Remove(ctx, domain.Transaction{}))

	other, err := f.svc.Create(ctx, "u1", rent("2027-01-10"), domain.Date{})
	require.NoError(t, err)
	require.NoError(t, f.svc.Remove(ctx, domain.Transaction{UserID: "u1", Recurrence: &domain.RecurrenceLink{ID: other.ID}}))
	assert.NotContains(t, f.store.recs, other.ID)

	used, active, err := f.svc.AccountUsage(ctx, "u1", "bank")
	require.NoError(t, err)
	assert.False(t, used || active)
	_, err = f.svc.Create(ctx, "u1", rent("2027-01-10"), domain.Date{})
	require.NoError(t, err)
	used, active, err = f.svc.AccountUsage(ctx, "u1", "bank")
	require.NoError(t, err)
	assert.True(t, used && active)

	f.store.err = errors.New("down")
	_, _, err = f.svc.AccountUsage(ctx, "u1", "bank")
	assert.ErrorContains(t, err, "check recurrence use")
	assert.ErrorContains(t, f.svc.Follow(ctx, occurrence, after), "find recurrence")
	assert.ErrorContains(t, f.svc.Remove(ctx, first), "delete recurrence")
}

func TestNewServiceDefaults(t *testing.T) {
	svc := NewService(newMemStore(), &memLedger{}, &refs{}, categoryFinder{&refs{}}, Options{})
	assert.Equal(t, DefaultMaxRecurrences, svc.max)
	assert.NotNil(t, svc.now())
	svc.metrics.TransactionCreated("expense")
	svc.Materialize(context.Background(), "u1", domain.Date{})
}

func TestEndBeforeKeepsAMovedRecurrence(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	r, err := f.svc.Create(ctx, "u1", rent("2026-08-05"), domain.Date{})
	require.NoError(t, err)
	require.Len(t, f.ledger.stored, 4, "August to November")
	// The start moves past them: they stay, linked to the recurrence.
	_, err = f.svc.Update(ctx, "u1", r.ID, domain.RecurrencePatch{StartDate: ptr("2027-01-10")}, domain.Date{})
	require.NoError(t, err)
	occurrence := f.ledger.stored[2]
	occurrence.UserID = "u1"

	f.ledger.earlierErr = errors.New("mongo down")
	assert.Error(t, f.svc.EndBefore(ctx, occurrence))
	f.ledger.earlierErr = nil

	// Cutting before the start with August and September left: ended, kept.
	require.NoError(t, f.svc.EndBefore(ctx, occurrence))
	kept := f.store.recs[r.ID]
	assert.Equal(t, "2026-10-04", kept.Rule.End.String())
	assert.Equal(t, "2027-01-10", kept.Rule.Start.String())
	assert.False(t, kept.Active())

	// From the first one, nothing is left before the cut: it goes.
	first := f.ledger.stored[0]
	first.UserID = "u1"
	require.NoError(t, f.svc.EndBefore(ctx, first))
	assert.NotContains(t, f.store.recs, r.ID)
}

func TestWritesRunTheHook(t *testing.T) {
	ctx := context.Background()
	f := newFixture()

	// A start in the past creates its history: the insert and the pass.
	r, err := f.svc.Create(ctx, "u1", rent("2026-08-05"), domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, 2, f.invalid["u1"])
	future, err := f.svc.Create(ctx, "u1", rent("2027-01-10"), domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, 3, f.invalid["u1"], "nothing due: the insert only")

	// Reads with nothing due, and patches that change nothing, never
	// run the hook.
	_, err = f.svc.List(ctx, "u1", domain.Date{})
	require.NoError(t, err)
	_, err = f.svc.Update(ctx, "u1", r.ID, domain.RecurrencePatch{Amount: ptr(r.Amount)}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, 3, f.invalid["u1"])

	_, err = f.svc.Update(ctx, "u1", future.ID, domain.RecurrencePatch{Amount: ptr(int64(1))}, domain.Date{})
	require.NoError(t, err)
	assert.Equal(t, 4, f.invalid["u1"])
	require.NoError(t, f.svc.Delete(ctx, "u1", future.ID))
	assert.Equal(t, 5, f.invalid["u1"])
	assert.Error(t, f.svc.Delete(ctx, "u1", future.ID))
	assert.Equal(t, 6, f.invalid["u1"], "an attempt counts: a write may have happened")

	// A pass that creates occurrences and then fails still runs the hook.
	f.store.recs[r.ID] = func() domain.Recurrence {
		x := f.store.recs[r.ID]
		x.Next = day(t, "2026-11-01")
		return x
	}()
	f.store.err = errors.New("mongo down")
	f.svc.Materialize(ctx, "u1", domain.Date{})
	assert.Equal(t, 7, f.invalid["u1"])
	assert.Zero(t, f.invalid["u2"])
}

func TestScheduled(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	_, err := f.svc.Create(ctx, "u1", rent("2026-08-05"), domain.Date{})
	require.NoError(t, err)
	salary := domain.RecurrenceInput{
		Kind: "income", AccountID: "bank", CategoryID: "salary", Amount: 900000, Frequency: "monthly", StartDate: "2026-09-01",
	}
	_, err = f.svc.Create(ctx, "u1", salary, domain.Date{})
	require.NoError(t, err)
	_, err = f.svc.Create(ctx, "u1", rent("2026-11-20"), domain.Date{})
	require.NoError(t, err)

	got, err := f.svc.Scheduled(ctx, "u1", day(t, "2026-11-01"), day(t, "2026-11-30"))
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-11-20"}, dates(got), "the rent of the 5th and the salary are created already")
	got, err = f.svc.Scheduled(ctx, "u1", day(t, "2026-12-01"), day(t, "2026-12-31"))
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-12-01", "2026-12-05", "2026-12-20"}, dates(got), "the earliest first")
	assert.Equal(t, domain.TransactionIncome, got[0].Kind)
	none, err := f.svc.Scheduled(ctx, "u2", day(t, "2026-12-01"), day(t, "2026-12-31"))
	require.NoError(t, err)
	assert.Equal(t, []domain.Transaction{}, none)

	f.store.err = errors.New("mongo down")
	_, err = f.svc.Scheduled(ctx, "u1", day(t, "2026-12-01"), day(t, "2026-12-31"))
	assert.Error(t, err)
}

func dates(ts []domain.Transaction) []string {
	out := []string{}
	for _, tx := range ts {
		out = append(out, tx.Date.String())
	}
	return out
}
