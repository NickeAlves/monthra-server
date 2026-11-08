package transactions

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// buy creates a purchase in n installments of the bank account and returns
// them, in order.
func (f fixture) buy(t *testing.T, n int) []domain.Transaction {
	t.Helper()
	in := expense()
	in.Installments, in.Amount, in.Description = n, 100000, "Sofa"
	first, err := f.svc.Create(context.Background(), "u1", in)
	require.NoError(t, err)
	list, err := f.store.Installments(context.Background(), "u1", first.Installment.GroupID, 1)
	require.NoError(t, err)
	require.Len(t, list, n)
	return list
}

func descriptions(list []domain.Transaction) []string {
	out := make([]string, len(list))
	for i, t := range list {
		out[i] = t.Description
	}
	return out
}

func TestCreateInstallments(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	in := expense()
	in.AccountID, in.Date, in.Amount, in.Installments = "card", "2026-11-24", 100000, 3
	first, err := f.svc.Create(ctx, "u1", in)
	require.NoError(t, err)
	assert.Equal(t, &domain.Installment{GroupID: "g1", Number: 1, Count: 3}, first.Installment, "the first installment")
	assert.Equal(t, int64(33334), first.Amount)
	assert.Equal(t, "2026-12", first.InvoiceMonth.String())
	assert.Equal(t, []string{"expense", "expense", "expense"}, f.metrics.created, "each installment is a transaction")
	assert.Equal(t, []string{"account:card", "category:food"}, f.refs.lookups, "looked up once")
	group, err := f.store.Installments(ctx, "u1", "g1", 1)
	require.NoError(t, err)
	require.Len(t, group, 3)
	assert.Equal(t, "2027-02", group[2].InvoiceMonth.String())

	income := domain.TransactionInput{Kind: "income", AccountID: "bank", CategoryID: "salary", Amount: 1000, Date: "2026-11-08", Installments: 2}
	_, err = f.svc.Create(ctx, "u1", income)
	assert.Equal(t, map[string]string{"installments": domain.CodeInvalid}, fields(t, err))

	f.store.insertErr = errors.New("boom")
	_, err = f.svc.Create(ctx, "u1", in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create transaction")
	assert.Len(t, f.metrics.created, 3, "nothing counted")
}

func TestUpdateInScope(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	list := f.buy(t, 4)

	got, err := f.svc.Update(ctx, "u1", list[1].ID, domain.TransactionPatch{Description: ptr("Couch")}, domain.ScopeFuture)
	require.NoError(t, err)
	assert.Equal(t, list[1].ID, got.ID, "answers the one addressed")
	assert.Equal(t, "Couch", got.Description)
	now, err := f.store.Installments(ctx, "u1", "g1", 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"Sofa", "Couch", "Couch", "Couch"}, descriptions(now), "the earlier installments stay")
	assert.Equal(t, 1, f.store.bulkUpdates, "one write")

	_, err = f.svc.Update(ctx, "u1", list[2].ID, domain.TransactionPatch{Description: ptr("Sofa")}, domain.ScopeAll)
	require.NoError(t, err)
	now, err = f.store.Installments(ctx, "u1", "g1", 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"Sofa", "Sofa", "Sofa", "Sofa"}, descriptions(now))

	_, err = f.svc.Update(ctx, "u1", list[0].ID, domain.TransactionPatch{Description: ptr("Chair")}, domain.ScopeThis)
	require.NoError(t, err)
	now, err = f.store.Installments(ctx, "u1", "g1", 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"Chair", "Sofa", "Sofa", "Sofa"}, descriptions(now))
	assert.Equal(t, 1, f.store.updates, "a single write")

	_, err = f.svc.Update(ctx, "u1", list[1].ID, domain.TransactionPatch{AccountID: ptr("wallet")}, domain.ScopeFuture)
	assert.Equal(t, map[string]string{"scope": domain.CodeInvalid}, fields(t, err), "a purchase stays on one account")
	moved, err := f.svc.Update(ctx, "u1", list[1].ID, domain.TransactionPatch{AccountID: ptr("wallet")}, domain.ScopeAll)
	require.NoError(t, err)
	assert.Equal(t, "wallet", moved.AccountID)
	now, err = f.store.Installments(ctx, "u1", "g1", 1)
	require.NoError(t, err)
	for _, inst := range now {
		assert.Equal(t, "wallet", inst.AccountID)
	}

	before := f.store.bulkUpdates
	_, err = f.svc.Update(ctx, "u1", list[1].ID, domain.TransactionPatch{Description: ptr("Sofa")}, domain.ScopeAll)
	require.NoError(t, err)
	assert.Equal(t, before, f.store.bulkUpdates, "nothing changed, nothing written")

	_, err = f.svc.Update(ctx, "u2", list[1].ID, domain.TransactionPatch{Description: ptr("x")}, domain.ScopeAll)
	assert.ErrorIs(t, err, domain.ErrNotFound)

	f.store.updateErr = errors.New("boom")
	_, err = f.svc.Update(ctx, "u1", list[1].ID, domain.TransactionPatch{Description: ptr("x")}, domain.ScopeAll)
	assert.ErrorContains(t, err, "update transaction")
	f.store.updateErr = nil

	f.store.err = errors.New("down")
	_, err = f.svc.Update(ctx, "u1", list[1].ID, domain.TransactionPatch{Description: ptr("x")}, domain.ScopeAll)
	assert.Error(t, err)
}

func TestUpdateInScopeOnACard(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	in := expense()
	in.AccountID, in.Date, in.Amount, in.Installments = "card", "2026-11-24", 30000, 3
	first, err := f.svc.Create(ctx, "u1", in)
	require.NoError(t, err)

	f.refs.lookups = nil
	_, err = f.svc.Update(ctx, "u1", first.ID, domain.TransactionPatch{Date: ptr("2026-11-26")}, domain.ScopeAll)
	require.NoError(t, err)
	assert.Equal(t, []string{"account:card"}, f.refs.lookups, "the card is looked up once")
	list, err := f.store.Installments(ctx, "u1", first.Installment.GroupID, 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"2027-01", "2027-02", "2027-03"},
		[]string{list[0].InvoiceMonth.String(), list[1].InvoiceMonth.String(), list[2].InvoiceMonth.String()})
	assert.Equal(t, "2027-01-26", list[2].Date.String())
}

func TestUpdateInScopeOutsideAPurchase(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	tx, err := f.svc.Create(ctx, "u1", expense())
	require.NoError(t, err)
	f.buy(t, 2)
	got, err := f.svc.Update(ctx, "u1", tx.ID, domain.TransactionPatch{AccountID: ptr("wallet")}, domain.ScopeAll)
	require.NoError(t, err)
	assert.Equal(t, "wallet", got.AccountID)
	assert.Equal(t, 1, f.store.updates)
	assert.Zero(t, f.store.bulkUpdates)
}

func TestDeleteInScope(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	list := f.buy(t, 4)

	assert.ErrorIs(t, f.svc.Delete(ctx, "u2", list[1].ID, domain.ScopeAll), domain.ErrNotFound)
	require.NoError(t, f.svc.Delete(ctx, "u1", list[2].ID, domain.ScopeThis))
	require.NoError(t, f.svc.Delete(ctx, "u1", list[1].ID, domain.ScopeFuture))
	left, err := f.store.Installments(ctx, "u1", "g1", 1)
	require.NoError(t, err)
	require.Len(t, left, 1)
	assert.Equal(t, 1, left[0].Installment.Number)

	require.NoError(t, f.svc.Delete(ctx, "u1", list[0].ID, domain.ScopeAll))
	left, err = f.store.Installments(ctx, "u1", "g1", 1)
	require.NoError(t, err)
	assert.Empty(t, left)
	assert.ErrorIs(t, f.svc.Delete(ctx, "u1", list[0].ID, domain.ScopeAll), domain.ErrNotFound)

	single, err := f.svc.Create(ctx, "u1", expense())
	require.NoError(t, err)
	require.NoError(t, f.svc.Delete(ctx, "u1", single.ID, domain.ScopeFuture), "a transaction outside a purchase goes alone")
	_, err = f.svc.Get(ctx, "u1", single.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}
