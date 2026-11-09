//go:build integration

package app

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/config"
)

func TestSeed(t *testing.T) {
	t.Setenv("RATE_LIMIT_API_USER", "1000/m")
	api, _, _, _ := startApp(t)
	cfg, err := config.Load()
	require.NoError(t, err)
	ctx := context.Background()
	native := &http.Client{Timeout: 10 * time.Second}
	logger := slog.New(slog.DiscardHandler)

	res, err := Seed(ctx, cfg, logger, "")
	require.NoError(t, err)
	assert.Equal(t, SeedEmail, res.Email)
	assert.GreaterOrEqual(t, len(res.Password), 20, "a random password")
	assert.Equal(t, 5, res.Accounts)
	assert.Equal(t, 5, res.Recurrences)
	assert.Greater(t, res.Transactions, 100)
	assert.Equal(t, 4, res.Budgets)

	asDemo := signIn(t, native, api, res.Email, res.Password)
	first := call(t, native, http.MethodGet, api+"/api/v1/dashboard/summary", nil, asDemo)
	require.Equal(t, http.StatusOK, first.status, first.body)
	brl := currencyOf(t, first, "BRL")
	assert.NotZero(t, brl["expense"])
	assert.NotEmpty(t, brl["expensesByCategory"])
	currencyOf(t, first, "USD")
	// The budgets of this month start in the state their limits were sized
	// for (with nothing spent yet, a budget is ok).
	r := call(t, native, http.MethodGet, api+"/api/v1/budgets", nil, asDemo)
	budgets, _ := r.body["budgets"].([]any)
	require.Len(t, budgets, 4, r.body)
	statuses := map[string]int{}
	for _, raw := range budgets {
		b, _ := raw.(map[string]any)
		status, _ := b["status"].(string)
		if spent, _ := b["spent"].(float64); spent == 0 {
			assert.Equal(t, "ok", status, b)
		}
		statuses[status]++
	}
	assert.Equal(t, 4, statuses["ok"]+statuses["warning"]+statuses["exceeded"])
	assert.LessOrEqual(t, statuses["exceeded"], 1)
	assert.LessOrEqual(t, statuses["warning"], 1)
	r = call(t, native, http.MethodGet, api+"/api/v1/me", nil, asDemo)
	require.Equal(t, http.StatusOK, r.status)
	firstID := r.str("id")

	// Again, with a password of the caller's: a new user, the same data.
	again, err := Seed(ctx, cfg, logger, "local demo password")
	require.NoError(t, err)
	assert.Equal(t, "local demo password", again.Password)
	assert.Equal(t, res.Transactions, again.Transactions)
	assert.Equal(t, 4, again.Budgets)
	r = call(t, native, http.MethodGet, api+"/api/v1/me", nil, asDemo)
	assert.Equal(t, http.StatusUnauthorized, r.status, "the old demo user is gone")
	asDemo = signIn(t, native, api, SeedEmail, "local demo password")
	r = call(t, native, http.MethodGet, api+"/api/v1/me", nil, asDemo)
	require.Equal(t, http.StatusOK, r.status)
	assert.NotEqual(t, firstID, r.str("id"))
	second := call(t, native, http.MethodGet, api+"/api/v1/dashboard/summary", nil, asDemo)
	require.Equal(t, http.StatusOK, second.status)
	for _, code := range []string{"BRL", "USD"} {
		before, after := currencyOf(t, first, code), currencyOf(t, second, code)
		for _, k := range []string{"income", "expense", "net", "balance", "scheduled", "history"} {
			assert.Equal(t, before[k], after[k], "the same %s of %s", k, code)
		}
	}

	_, err = Seed(ctx, cfg, logger, "short")
	assert.Error(t, err)
	staging := cfg
	staging.AppEnv = config.EnvStaging
	_, err = Seed(ctx, staging, logger, "")
	assert.ErrorIs(t, err, ErrSeedNotLocal)
}
