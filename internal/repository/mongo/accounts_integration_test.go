//go:build integration

package mongo_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/NickeAlves/monthra-server/internal/domain"
	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

func newAccount(t *testing.T, userID, name string, in domain.AccountInput, at time.Time) domain.Account {
	t.Helper()
	in.Name = name
	if in.Type == "" {
		in.Type, in.Currency = "checking", "BRL"
	}
	a, err := domain.NewAccount(userID, in, at)
	require.NoError(t, err)
	return a
}

func TestAccountsLifecycle(t *testing.T) {
	ctx := context.Background()
	accounts := repo.NewAccounts(testDB(t))
	owner := bson.NewObjectID().Hex()

	limit := int64(500000)
	card, err := accounts.Insert(ctx, newAccount(t, owner, "Card", domain.AccountInput{
		Type: "credit_card", Currency: "USD", InitialBalance: -1200,
		Card: &domain.CardSettings{ClosingDay: 28, DueDay: 5, Limit: &limit},
	}, now))
	require.NoError(t, err)
	require.Len(t, card.ID, 24)

	got, err := accounts.ByID(ctx, owner, card.ID)
	require.NoError(t, err)
	assert.Equal(t, card, got, "every field round-trips")
	assert.Equal(t, domain.SourceManual, got.Source)

	bank, err := accounts.Insert(ctx, newAccount(t, owner, "Bank", domain.AccountInput{}, now.Add(time.Minute)))
	require.NoError(t, err)
	assert.Nil(t, bank.Card)

	archivedAt := now.Add(time.Hour)
	bank.Name, bank.InitialBalance, bank.ArchivedAt, bank.UpdatedAt = "Old bank", 42, &archivedAt, archivedAt
	bank.Currency, bank.Type = "EUR", domain.AccountCash // never stored by Update
	require.NoError(t, accounts.Update(ctx, bank))

	stored, err := accounts.ByID(ctx, owner, bank.ID)
	require.NoError(t, err)
	assert.Equal(t, "Old bank", stored.Name)
	assert.Equal(t, int64(42), stored.InitialBalance)
	assert.Equal(t, archivedAt, *stored.ArchivedAt)
	assert.Equal(t, archivedAt, stored.UpdatedAt)
	assert.Equal(t, "BRL", stored.Currency, "currency is immutable")
	assert.Equal(t, domain.AccountChecking, stored.Type, "type is immutable")

	active, err := accounts.List(ctx, owner, false)
	require.NoError(t, err)
	assert.Equal(t, []string{card.ID}, ids(active))
	all, err := accounts.List(ctx, owner, true)
	require.NoError(t, err)
	assert.Equal(t, []string{card.ID, bank.ID}, ids(all), "oldest first")

	n, err := accounts.Count(ctx, owner)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	require.NoError(t, accounts.Delete(ctx, owner, bank.ID))
	assert.ErrorIs(t, accounts.Delete(ctx, owner, bank.ID), domain.ErrNotFound)
	_, err = accounts.ByID(ctx, owner, bank.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func ids(list []domain.Account) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.ID
	}
	return out
}

func TestAccountsAreScopedToTheirOwner(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	accounts := repo.NewAccounts(db)
	owner, intruder := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()

	a, err := accounts.Insert(ctx, newAccount(t, owner, "Main", domain.AccountInput{}, now))
	require.NoError(t, err)

	_, err = accounts.ByID(ctx, intruder, a.ID)
	assert.ErrorIs(t, err, domain.ErrNotFound)

	stolen := a
	stolen.UserID, stolen.Name = intruder, "Mine now"
	assert.ErrorIs(t, accounts.Update(ctx, stolen), domain.ErrNotFound)
	assert.ErrorIs(t, accounts.Delete(ctx, intruder, a.ID), domain.ErrNotFound)

	list, err := accounts.List(ctx, intruder, true)
	require.NoError(t, err)
	assert.Empty(t, list)
	n, err := accounts.Count(ctx, intruder)
	require.NoError(t, err)
	assert.Zero(t, n)

	got, err := accounts.ByID(ctx, owner, a.ID)
	require.NoError(t, err)
	assert.Equal(t, "Main", got.Name, "untouched")

	for _, bad := range []string{"", "not-an-id", "zzzzzzzzzzzzzzzzzzzzzzzz"} {
		_, err = accounts.ByID(ctx, owner, bad)
		assert.ErrorIs(t, err, domain.ErrNotFound, bad)
		_, err = accounts.ByID(ctx, bad, a.ID)
		assert.ErrorIs(t, err, domain.ErrNotFound, bad)
	}
	list, err = accounts.List(ctx, "not-an-id", true)
	require.NoError(t, err)
	assert.Empty(t, list)

	eraser := repo.NewUserEraser(db, repo.CollAccounts)
	require.NoError(t, eraser.EraseUserData(ctx, owner))
	n, err = accounts.Count(ctx, owner)
	require.NoError(t, err)
	assert.Zero(t, n)
}
