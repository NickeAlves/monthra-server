package transactions

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 123456789, time.UTC)

// memStore keeps transactions in memory, scoped by user like the real store.
type memStore struct {
	txs         map[string]domain.Transaction
	order       []string
	next        int
	err         error
	insertErr   error
	updateErr   error
	updates     int
	bulkUpdates int
	groups      int
	lastLimit   int
	lastAfter   *domain.TransactionCursor
	lastQuery   domain.TransactionFilter
}

func newMemStore() *memStore { return &memStore{txs: map[string]domain.Transaction{}} }

func (m *memStore) Insert(_ context.Context, t domain.Transaction) (domain.Transaction, error) {
	if m.insertErr != nil {
		return domain.Transaction{}, m.insertErr
	}
	m.next++
	t.ID = fmt.Sprintf("t%d", m.next)
	m.txs[t.ID] = t
	m.order = append(m.order, t.ID)
	return t, nil
}

func (m *memStore) ByID(_ context.Context, userID, id string) (domain.Transaction, error) {
	if m.err != nil {
		return domain.Transaction{}, m.err
	}
	t, ok := m.txs[id]
	if !ok || t.UserID != userID {
		return domain.Transaction{}, domain.ErrNotFound
	}
	return t, nil
}

func (m *memStore) Update(ctx context.Context, t domain.Transaction) error {
	if _, err := m.ByID(ctx, t.UserID, t.ID); err != nil {
		return err
	}
	m.updates++
	m.txs[t.ID] = t
	return nil
}

func (m *memStore) Delete(ctx context.Context, userID, id string) error {
	if _, err := m.ByID(ctx, userID, id); err != nil {
		return err
	}
	delete(m.txs, id)
	return nil
}

func (m *memStore) InsertInstallments(ctx context.Context, ts []domain.Transaction) ([]domain.Transaction, error) {
	m.groups++
	out := make([]domain.Transaction, len(ts))
	for i, t := range ts {
		inst := *t.Installment
		inst.GroupID = fmt.Sprintf("g%d", m.groups)
		t.Installment = &inst
		created, err := m.Insert(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = created
	}
	return out, nil
}

func (m *memStore) Installments(_ context.Context, userID, groupID string, from int) ([]domain.Transaction, error) {
	if m.err != nil {
		return nil, m.err
	}
	var out []domain.Transaction
	for _, id := range m.order {
		t, ok := m.txs[id]
		if ok && t.UserID == userID && t.Installment != nil && t.Installment.GroupID == groupID && t.Installment.Number >= from {
			out = append(out, t)
		}
	}
	return out, nil
}

// UpdateMany stores ts; with updateErr set, it stores the first one only
// and fails, as a write interrupted midway.
func (m *memStore) UpdateMany(ctx context.Context, ts []domain.Transaction) error {
	m.bulkUpdates++
	for _, t := range ts {
		if _, err := m.ByID(ctx, t.UserID, t.ID); err == nil {
			m.txs[t.ID] = t
		}
		if m.updateErr != nil {
			return m.updateErr
		}
	}
	return nil
}

func (m *memStore) DeleteInstallments(ctx context.Context, userID, groupID string, from int) error {
	list, err := m.Installments(ctx, userID, groupID, from)
	if err != nil {
		return err
	}
	for _, t := range list {
		delete(m.txs, t.ID)
	}
	return nil
}

func (m *memStore) Occurrences(_ context.Context, userID, recurrenceID string, from domain.Date) ([]domain.Transaction, error) {
	if m.err != nil {
		return nil, m.err
	}
	var out []domain.Transaction
	for _, id := range m.order {
		t, ok := m.txs[id]
		if ok && t.UserID == userID && t.Recurrence != nil && t.Recurrence.ID == recurrenceID && !t.Recurrence.Date.Before(from) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *memStore) DeleteOccurrences(ctx context.Context, userID, recurrenceID string, from domain.Date) error {
	list, err := m.Occurrences(ctx, userID, recurrenceID, from)
	if err != nil {
		return err
	}
	for _, t := range list {
		delete(m.txs, t.ID)
	}
	return nil
}

// List returns the caller's transactions newest inserted first.
func (m *memStore) List(_ context.Context, userID string, f domain.TransactionFilter, after *domain.TransactionCursor, limit int) ([]domain.Transaction, error) {
	m.lastLimit, m.lastAfter, m.lastQuery = limit, after, f
	if m.err != nil {
		return nil, m.err
	}
	var out []domain.Transaction
	for i := len(m.order) - 1; i >= 0 && len(out) < limit; i-- {
		if t, ok := m.txs[m.order[i]]; ok && t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}

// refs finds accounts and categories by id and owner, and counts lookups.
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

// categoryFinder adapts refs to the Categories port.
type categoryFinder struct{ *refs }

func (c categoryFinder) ByID(_ context.Context, userID, id string) (domain.Category, error) {
	c.lookups = append(c.lookups, "category:"+id)
	if c.err != nil {
		return domain.Category{}, c.err
	}
	cat, ok := c.categories[id]
	if !ok || cat.UserID != userID {
		return domain.Category{}, domain.ErrNotFound
	}
	return cat, nil
}

type countingMetrics struct{ created []string }

func (m *countingMetrics) TransactionCreated(kind string) { m.created = append(m.created, kind) }

// fakeSeries records what the transactions asked of the recurrences.
type fakeSeries struct {
	calls []string
	err   error
}

func (f *fakeSeries) Materialize(_ context.Context, userID string, asOf domain.Date) {
	f.calls = append(f.calls, "materialize:"+userID+"@"+asOf.String())
}

func (f *fakeSeries) Follow(_ context.Context, before, after domain.Transaction) error {
	f.calls = append(f.calls, fmt.Sprintf("follow:%s:%d->%d", after.Recurrence.ID, before.Amount, after.Amount))
	return f.err
}

func (f *fakeSeries) EndBefore(_ context.Context, o domain.Transaction) error {
	f.calls = append(f.calls, "end:"+o.Recurrence.ID+"@"+o.Recurrence.Date.String())
	return f.err
}

func (f *fakeSeries) Remove(_ context.Context, o domain.Transaction) error {
	f.calls = append(f.calls, "remove:"+o.Recurrence.ID)
	return f.err
}

type fixture struct {
	svc     *Service
	store   *memStore
	refs    *refs
	metrics *countingMetrics
	series  *fakeSeries
	invalid countingHook
}

func newFixture() fixture {
	r := &refs{
		accounts: map[string]domain.Account{
			"bank":    {ID: "bank", UserID: "u1", Currency: "BRL"},
			"wallet":  {ID: "wallet", UserID: "u1", Currency: "BRL"},
			"dollars": {ID: "dollars", UserID: "u1", Currency: "USD"},
			"bobs":    {ID: "bobs", UserID: "u2", Currency: "BRL"},
			"card": {
				ID: "card", UserID: "u1", Type: domain.AccountCreditCard, Currency: "BRL",
				Card: &domain.CardSettings{ClosingDay: 25, DueDay: 5},
			},
		},
		categories: map[string]domain.Category{
			"food":   {ID: "food", UserID: "u1", Kind: domain.CategoryExpense},
			"fun":    {ID: "fun", UserID: "u1", Kind: domain.CategoryExpense},
			"salary": {ID: "salary", UserID: "u1", Kind: domain.CategoryIncome},
		},
	}
	store, metrics, series, invalid := newMemStore(), &countingMetrics{}, &fakeSeries{}, countingHook{}
	svc := NewService(store, r, categoryFinder{r}, Options{
		Now: func() time.Time { return t0 }, Metrics: metrics, Series: series, AfterWrite: invalid,
	})
	return fixture{svc: svc, store: store, refs: r, metrics: metrics, series: series, invalid: invalid}
}

// countingHook counts the hook calls of each user.
type countingHook map[string]int

func (c countingHook) AfterWrite(_ context.Context, userID string) { c[userID]++ }

func expense() domain.TransactionInput {
	return domain.TransactionInput{Kind: "expense", AccountID: "bank", CategoryID: "food", Amount: 4590, Date: "2026-11-08"}
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

func TestCreate(t *testing.T) {
	f := newFixture()
	tx, err := f.svc.Create(context.Background(), "u1", expense())
	require.NoError(t, err)
	assert.Equal(t, "t1", tx.ID)
	assert.Equal(t, "BRL", tx.Currency)
	assert.Equal(t, t0.Truncate(time.Millisecond), tx.CreatedAt, "millisecond time")
	assert.Equal(t, []string{"account:bank", "category:food"}, f.refs.lookups)
	assert.Equal(t, []string{"expense"}, f.metrics.created)

	f.refs.lookups = nil
	_, err = f.svc.Create(context.Background(), "u1", domain.TransactionInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "dollars", CategoryID: "food", Amount: 1000, ToAmount: ptr(int64(200)), Date: "2026-11-08",
	})
	require.Error(t, err)
	assert.Equal(t, map[string]string{"categoryId": domain.CodeInvalid}, fields(t, err), "the category of a transfer is never looked up, only refused")
	assert.Equal(t, []string{"account:bank", "account:dollars"}, f.refs.lookups)

	tx, err = f.svc.Create(context.Background(), "u1", domain.TransactionInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "dollars", Amount: 1000, ToAmount: ptr(int64(200)), Date: "2026-11-08",
	})
	require.NoError(t, err)
	assert.Equal(t, "USD", tx.ToCurrency)
	assert.Equal(t, []string{"expense", "transfer"}, f.metrics.created)
}

func TestCreateRefusesWhatTheCallerDoesNotHold(t *testing.T) {
	f := newFixture()
	in := expense()
	in.AccountID, in.CategoryID = "bobs", "ghost"
	_, err := f.svc.Create(context.Background(), "u1", in)
	assert.Equal(t, map[string]string{"accountId": domain.CodeInvalid, "categoryId": domain.CodeInvalid}, fields(t, err),
		"another user's account reads like one that does not exist")
	assert.Empty(t, f.store.txs)
	assert.Empty(t, f.metrics.created)

	_, err = f.svc.Create(context.Background(), "u2", expense())
	assert.Equal(t, map[string]string{"accountId": domain.CodeInvalid, "categoryId": domain.CodeInvalid}, fields(t, err))
}

func TestCreateFailures(t *testing.T) {
	f := newFixture()
	f.store.insertErr = errors.New("mongo down")
	_, err := f.svc.Create(context.Background(), "u1", expense())
	require.ErrorContains(t, err, "create transaction: mongo down")
	assert.Empty(t, f.metrics.created, "counted only once stored")

	f = newFixture()
	f.refs.err = errors.New("timeout")
	_, err = f.svc.Create(context.Background(), "u1", expense())
	require.ErrorContains(t, err, "find account: timeout", "a failed lookup is not a validation error")
	var ve *domain.ValidationError
	assert.NotErrorAs(t, err, &ve)

	f.refs.err = nil
	f.svc.categories = failingCategories{categoryFinder{f.refs}}
	_, err = f.svc.Create(context.Background(), "u1", expense())
	require.ErrorContains(t, err, "find category: boom")
	f.refs.lookups = nil
	f.svc.accounts = failAfter{f.refs, 1}
	_, err = f.svc.Create(context.Background(), "u1", domain.TransactionInput{Kind: "transfer", AccountID: "bank", ToAccountID: "wallet", Amount: 1, Date: "2026-11-08"})
	require.ErrorContains(t, err, "find account: boom", "the destination lookup")
}

type failingCategories struct{ categoryFinder }

func (failingCategories) ByID(context.Context, string, string) (domain.Category, error) {
	return domain.Category{}, errors.New("boom")
}

// failAfter answers the first n account lookups, then fails.
type failAfter struct {
	*refs
	n int
}

func (f failAfter) ByID(ctx context.Context, userID, id string) (domain.Account, error) {
	if len(f.lookups) >= f.n {
		return domain.Account{}, errors.New("boom")
	}
	return f.refs.ByID(ctx, userID, id)
}

func TestUpdateLooksUpOnlyWhatMoves(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	tx, err := f.svc.Create(ctx, "u1", expense())
	require.NoError(t, err)

	f.refs.lookups = nil
	got, err := f.svc.Update(ctx, "u1", tx.ID, domain.TransactionPatch{AccountID: ptr("bank"), CategoryID: ptr("food")}, domain.ScopeThis)
	require.NoError(t, err)
	assert.Equal(t, tx, got)
	assert.Empty(t, f.refs.lookups, "the same references")
	assert.Zero(t, f.store.updates, "nothing changed, nothing written")

	got, err = f.svc.Update(ctx, "u1", tx.ID, domain.TransactionPatch{AccountID: ptr("dollars"), CategoryID: ptr("fun"), Amount: ptr(int64(1))}, domain.ScopeThis)
	require.NoError(t, err)
	assert.Equal(t, []string{"account:dollars", "category:fun"}, f.refs.lookups)
	assert.Equal(t, "USD", got.Currency)
	assert.Equal(t, 1, f.store.updates)
	assert.Equal(t, got, f.store.txs[tx.ID])

	_, err = f.svc.Update(ctx, "u1", tx.ID, domain.TransactionPatch{AccountID: ptr("bobs")}, domain.ScopeThis)
	assert.Equal(t, map[string]string{"accountId": domain.CodeInvalid}, fields(t, err))
	_, err = f.svc.Update(ctx, "u1", tx.ID, domain.TransactionPatch{AccountID: ptr("")}, domain.ScopeThis)
	assert.Equal(t, map[string]string{"accountId": domain.CodeRequired}, fields(t, err))

	_, err = f.svc.Update(ctx, "u2", tx.ID, domain.TransactionPatch{Amount: ptr(int64(2))}, domain.ScopeThis)
	assert.ErrorIs(t, err, domain.ErrNotFound)

	transfer, err := f.svc.Create(ctx, "u1", domain.TransactionInput{Kind: "transfer", AccountID: "bank", ToAccountID: "wallet", Amount: 10, Date: "2026-11-08"})
	require.NoError(t, err)
	f.refs.lookups = nil
	_, err = f.svc.Update(ctx, "u1", transfer.ID, domain.TransactionPatch{ToAccountID: ptr("dollars"), ToAmount: ptr(int64(2)), CategoryID: ptr("food")}, domain.ScopeThis)
	require.Error(t, err)
	assert.Equal(t, map[string]string{"categoryId": domain.CodeInvalid}, fields(t, err))
	assert.Equal(t, []string{"account:dollars"}, f.refs.lookups, "a transfer's category is never looked up")
	got, err = f.svc.Update(ctx, "u1", transfer.ID, domain.TransactionPatch{ToAccountID: ptr("dollars"), ToAmount: ptr(int64(2))}, domain.ScopeThis)
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.ToAmount)

	f.refs.err = errors.New("timeout")
	_, err = f.svc.Update(ctx, "u1", transfer.ID, domain.TransactionPatch{ToAccountID: ptr("wallet")}, domain.ScopeThis)
	assert.ErrorContains(t, err, "find account: timeout")
	_, err = f.svc.Update(ctx, "u1", transfer.ID, domain.TransactionPatch{AccountID: ptr("wallet")}, domain.ScopeThis)
	assert.ErrorContains(t, err, "find account: timeout")
	_, err = f.svc.Update(ctx, "u1", tx.ID, domain.TransactionPatch{CategoryID: ptr("food")}, domain.ScopeThis)
	assert.ErrorContains(t, err, "find category: timeout")
}

func TestUpdateLooksUpTheCardOfAnInvoice(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	in := expense()
	in.AccountID, in.Date = "card", "2026-11-24"
	purchase, err := f.svc.Create(ctx, "u1", in)
	require.NoError(t, err)
	assert.Equal(t, "2026-12", purchase.InvoiceMonth.String(), "the invoice of the day")

	f.refs.lookups = nil
	_, err = f.svc.Update(ctx, "u1", purchase.ID, domain.TransactionPatch{Description: ptr("lunch"), Date: ptr("2026-11-24")}, domain.ScopeThis)
	require.NoError(t, err)
	assert.Empty(t, f.refs.lookups, "the same day needs no settings")

	got, err := f.svc.Update(ctx, "u1", purchase.ID, domain.TransactionPatch{Date: ptr("2026-11-26")}, domain.ScopeThis)
	require.NoError(t, err)
	assert.Equal(t, []string{"account:card"}, f.refs.lookups, "a new day on a card needs its settings")
	assert.Equal(t, "2027-01", got.InvoiceMonth.String())

	f.refs.lookups = nil
	got, err = f.svc.Update(ctx, "u1", purchase.ID, domain.TransactionPatch{InvoiceMonth: ptr("2027-02")}, domain.ScopeThis)
	require.NoError(t, err)
	assert.Equal(t, []string{"account:card"}, f.refs.lookups)
	assert.Equal(t, "2027-02", got.InvoiceMonth.String())
	assert.Equal(t, got, f.store.txs[purchase.ID])

	plain, err := f.svc.Create(ctx, "u1", expense())
	require.NoError(t, err)
	f.refs.lookups = nil
	_, err = f.svc.Update(ctx, "u1", plain.ID, domain.TransactionPatch{Date: ptr("2026-11-01")}, domain.ScopeThis)
	require.NoError(t, err)
	assert.Empty(t, f.refs.lookups, "a leg off a card has no invoice to pick")
	_, err = f.svc.Update(ctx, "u1", plain.ID, domain.TransactionPatch{InvoiceMonth: ptr("2026-12")}, domain.ScopeThis)
	assert.Equal(t, map[string]string{"invoiceMonth": domain.CodeInvalid}, fields(t, err))
	assert.Equal(t, []string{"account:bank"}, f.refs.lookups, "a named invoice needs the account")

	payment, err := f.svc.Create(ctx, "u1", domain.TransactionInput{
		Kind: "transfer", AccountID: "bank", ToAccountID: "card", Amount: 5000, Date: "2026-12-05",
	})
	require.NoError(t, err)
	assert.Equal(t, "2026-12", payment.ToInvoiceMonth.String())
	f.refs.lookups = nil
	got, err = f.svc.Update(ctx, "u1", payment.ID, domain.TransactionPatch{Date: ptr("2026-12-26")}, domain.ScopeThis)
	require.NoError(t, err)
	assert.Equal(t, []string{"account:card"}, f.refs.lookups, "only the card leg")
	assert.Equal(t, "2027-01", got.ToInvoiceMonth.String())

	f.refs.err = errors.New("timeout")
	_, err = f.svc.Update(ctx, "u1", purchase.ID, domain.TransactionPatch{Date: ptr("2026-11-01")}, domain.ScopeThis)
	assert.ErrorContains(t, err, "find account: timeout")
	_, err = f.svc.Update(ctx, "u1", payment.ID, domain.TransactionPatch{ToInvoiceMonth: ptr("2026-12")}, domain.ScopeThis)
	assert.ErrorContains(t, err, "find account: timeout")
}

func TestGetAndDelete(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	tx, err := f.svc.Create(ctx, "u1", expense())
	require.NoError(t, err)

	got, err := f.svc.Get(ctx, "u1", tx.ID)
	require.NoError(t, err)
	assert.Equal(t, tx, got)
	_, err = f.svc.Get(ctx, "u2", tx.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)

	assert.ErrorIs(t, f.svc.Delete(ctx, "u2", tx.ID, domain.ScopeThis), domain.ErrNotFound)
	require.NoError(t, f.svc.Delete(ctx, "u1", tx.ID, domain.ScopeThis))
	assert.ErrorIs(t, f.svc.Delete(ctx, "u1", tx.ID, domain.ScopeThis), domain.ErrNotFound)
}

func TestList(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	for range 5 {
		_, err := f.svc.Create(ctx, "u1", expense())
		require.NoError(t, err)
	}

	page, err := f.svc.List(ctx, "u1", Query{})
	require.NoError(t, err)
	assert.Equal(t, DefaultPageSize+1, f.store.lastLimit, "one more tells whether a page follows")
	assert.Len(t, page.Items, 5)
	assert.Nil(t, page.Next, "the last page")

	_, err = f.svc.List(ctx, "u1", Query{Limit: MaxPageSize})
	require.NoError(t, err)
	assert.Equal(t, MaxPageSize+1, f.store.lastLimit)
	for _, limit := range []int{-1, MaxPageSize + 1} {
		_, err = f.svc.List(ctx, "u1", Query{Limit: limit})
		assert.Equal(t, map[string]string{"limit": domain.CodeOutOfRange}, fields(t, err), limit)
	}

	filter := domain.TransactionFilter{Kind: domain.TransactionExpense}
	after := &domain.TransactionCursor{ID: "t9"}
	page, err = f.svc.List(ctx, "u1", Query{Filter: filter, After: after, Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, filter, f.store.lastQuery)
	assert.Same(t, after, f.store.lastAfter)
	assert.Equal(t, []string{"t5", "t4"}, []string{page.Items[0].ID, page.Items[1].ID})
	require.NotNil(t, page.Next)
	assert.Equal(t, domain.TransactionCursor{Date: page.Items[1].Date, ID: "t4"}, *page.Next, "where the page ended")

	page, err = f.svc.List(ctx, "u2", Query{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)

	f.store.err = errors.New("mongo down")
	_, err = f.svc.List(ctx, "u1", Query{})
	assert.ErrorContains(t, err, "list transactions: mongo down")
}

func TestDefaults(t *testing.T) {
	svc := NewService(newMemStore(), &refs{}, categoryFinder{&refs{}}, Options{})
	assert.NotNil(t, svc.now)
	assert.NotPanics(t, func() { svc.metrics.TransactionCreated("expense") })
}

func ptr[T any](v T) *T { return &v }

func TestWritesRunTheHook(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	bad := expense()
	bad.Amount = 0
	_, err := f.svc.Create(ctx, "u1", bad)
	require.Error(t, err)
	assert.Zero(t, f.invalid["u1"], "nothing written")

	tx, err := f.svc.Create(ctx, "u1", expense())
	require.NoError(t, err)
	assert.Equal(t, 1, f.invalid["u1"])
	purchase := expense()
	purchase.Installments = 3
	first, err := f.svc.Create(ctx, "u1", purchase)
	require.NoError(t, err)
	assert.Equal(t, 2, f.invalid["u1"])

	_, err = f.svc.Update(ctx, "u1", tx.ID, domain.TransactionPatch{Amount: ptr(tx.Amount)}, domain.ScopeThis)
	require.NoError(t, err)
	assert.Equal(t, 2, f.invalid["u1"], "a patch that changes nothing writes nothing")
	_, err = f.svc.Update(ctx, "u1", tx.ID, domain.TransactionPatch{Amount: ptr(int64(1))}, domain.ScopeThis)
	require.NoError(t, err)
	assert.Equal(t, 3, f.invalid["u1"])
	_, err = f.svc.Update(ctx, "u1", first.ID, domain.TransactionPatch{Amount: ptr(int64(100))}, domain.ScopeAll)
	require.NoError(t, err)
	assert.Equal(t, 4, f.invalid["u1"], "once for the whole purchase")

	require.NoError(t, f.svc.Delete(ctx, "u1", first.ID, domain.ScopeFuture))
	assert.Equal(t, 5, f.invalid["u1"])
	require.NoError(t, f.svc.Delete(ctx, "u1", tx.ID, domain.ScopeThis))
	assert.Equal(t, 6, f.invalid["u1"])

	// A failed write still runs the hook: part of it may be stored.
	f.store.insertErr = errors.New("mongo down")
	_, err = f.svc.Create(ctx, "u1", expense())
	require.Error(t, err)
	assert.Equal(t, 7, f.invalid["u1"])
	assert.Zero(t, f.invalid["u2"])
}
