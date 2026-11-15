//go:build integration

package mongo_test

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/NickeAlves/monthra-server/internal/audit"
	"github.com/NickeAlves/monthra-server/internal/domain"
	repo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
)

// collect ranges over seq, failing the test on an error.
func collect[T any](t *testing.T, seq iter.Seq2[T, error]) []T {
	t.Helper()
	var out []T
	for v, err := range seq {
		require.NoError(t, err)
		out = append(out, v)
	}
	return out
}

func TestSessionsForUser(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	sessions := repo.NewSessions(db)
	owner, other := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	var ids []string
	for i := range 3 {
		at := now.Add(time.Duration(i) * time.Hour)
		s, err := sessions.Create(ctx, domain.Session{UserID: owner, UserAgent: "ua", IP: "198.51.100.7", CreatedAt: at, LastUsedAt: at, ExpiresAt: at.Add(time.Hour)})
		require.NoError(t, err)
		ids = append(ids, s.ID)
	}
	require.NoError(t, sessions.Revoke(ctx, ids[1], now.Add(90*time.Minute)))
	_, err := sessions.Create(ctx, domain.Session{UserID: other, CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour)})
	require.NoError(t, err)

	var got []domain.Session
	op := profileOn(t, db, repo.CollSessions, func() { got = collect(t, sessions.ForUser(ctx, owner)) })
	require.Len(t, got, 3, "revoked and expired ones too, never another user's")
	assert.Equal(t, []string{ids[2], ids[1], ids[0]}, []string{got[0].ID, got[1].ID, got[2].ID}, "newest first")
	require.NotNil(t, got[1].RevokedAt)
	assert.Equal(t, now.Add(90*time.Minute), got[1].RevokedAt.UTC())
	assert.Equal(t, 3, op.KeysExamined, "reads only the owner's entries: %s", op.PlanSummary)
	assert.Contains(t, op.PlanSummary, "IXSCAN { user_id: 1, created_at: -1 }")

	assert.Empty(t, collect(t, sessions.ForUser(ctx, "bad")), "an id that cannot exist owns none")
}

func TestAuditLogsForUser(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	logs := repo.NewAuditLogs(db)
	owner, sid := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	require.NoError(t, logs.Insert(ctx, audit.Entry{Event: audit.EventLoginSuccess, UserID: owner, SessionID: sid, IP: "198.51.100.7", UserAgent: "ua", CreatedAt: now}))
	require.NoError(t, logs.Insert(ctx, audit.Entry{Event: audit.EventReauthFailure, UserID: owner, Reason: "wrong_password", CreatedAt: now.Add(time.Minute)}))
	require.NoError(t, logs.Insert(ctx, audit.Entry{Event: audit.EventLoginSuccess, UserID: bson.NewObjectID().Hex(), CreatedAt: now}))
	require.NoError(t, logs.Insert(ctx, audit.Entry{Event: audit.EventLoginFailure, Reason: "unknown_user", CreatedAt: now}))

	var got []audit.Entry
	op := profileOn(t, db, repo.CollAuditLogs, func() { got = collect(t, logs.ForUser(ctx, owner)) })
	require.Len(t, got, 2)
	assert.Equal(t, audit.Entry{Event: audit.EventReauthFailure, UserID: owner, Reason: "wrong_password", CreatedAt: now.Add(time.Minute)}, withUTC(got[0]), "newest first")
	assert.Equal(t, audit.Entry{Event: audit.EventLoginSuccess, UserID: owner, SessionID: sid, IP: "198.51.100.7", UserAgent: "ua", CreatedAt: now}, withUTC(got[1]))
	assert.Equal(t, 2, op.KeysExamined, "reads only the owner's entries: %s", op.PlanSummary)
	assert.Contains(t, op.PlanSummary, "IXSCAN { user_id: 1, created_at: -1 }")

	assert.Empty(t, collect(t, logs.ForUser(ctx, "bad")))
}

func withUTC(e audit.Entry) audit.Entry {
	e.CreatedAt = e.CreatedAt.UTC()
	return e
}

func TestBudgetsAll(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	budgets := repo.NewBudgets(db)
	owner, other := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	food, fun := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	// Inserted out of order: the later month first, the later category first.
	for _, month := range []domain.Month{budgetsMonth, budgetsMonth.AddMonths(-1)} {
		for _, category := range []string{fun, food} {
			b, err := domain.NewBudget(owner, domain.BudgetInput{CategoryID: category, Currency: "BRL", Limit: 1000}, month, now)
			require.NoError(t, err)
			_, err = budgets.Insert(ctx, b)
			require.NoError(t, err)
		}
	}
	_, err := budgets.Insert(ctx, newBudget(t, other, food, 1000))
	require.NoError(t, err)

	var got []domain.Budget
	op := profileOn(t, db, repo.CollBudgets, func() { got = collect(t, budgets.All(ctx, owner)) })
	require.Len(t, got, 4, "every month, never another user's")
	type key struct {
		month    domain.Month
		category string
	}
	keys := make([]key, len(got))
	for i, b := range got {
		keys[i] = key{b.Month, b.CategoryID}
	}
	first, second := food, fun
	if fun < food {
		first, second = fun, food
	}
	previous := budgetsMonth.AddMonths(-1)
	assert.Equal(t, []key{{previous, first}, {previous, second}, {budgetsMonth, first}, {budgetsMonth, second}}, keys,
		"by month, then by category")
	assert.Equal(t, 4, op.KeysExamined, "reads only the owner's entries: %s", op.PlanSummary)
	assert.Contains(t, op.PlanSummary, "IXSCAN { user_id: 1, month: 1, category_id: 1 }")

	for _, err := range budgets.All(ctx, owner) {
		require.NoError(t, err)
		break // stopping early closes the cursor
	}
	assert.Empty(t, collect(t, budgets.All(ctx, "bad")))
}
