//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRepositoryClearModelRateLimitPreservesInterleavedUpdates(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	account := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "credits-key-cas", Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-only"},
	})
	loadLimits := func() map[string]any {
		current, err := repo.GetByID(ctx, account.ID)
		require.NoError(t, err)
		return current.Extra["model_rate_limits"].(map[string]any)
	}
	countOutbox := func() int {
		var count int
		require.NoError(t, scanSingleRow(ctx, tx, "SELECT count(*) FROM scheduler_outbox WHERE account_id = $1", []any{account.ID}, &count))
		return count
	}

	require.NoError(t, repo.SetModelRateLimit(ctx, account.ID, "AICredits", time.Now().Add(-time.Minute)))
	expected := loadLimits()["AICredits"].(map[string]any)
	// Another request writes a different model after the credits request took
	// its snapshot. Removing credits must leave this newer cooldown untouched.
	require.NoError(t, repo.SetModelRateLimit(ctx, account.ID, "claude-sonnet-4-6", time.Now().Add(time.Hour)))
	concurrentLimit := loadLimits()["claude-sonnet-4-6"]
	before := countOutbox()
	cleared, err := repo.ClearModelRateLimitIfUnchanged(ctx, account.ID, "AICredits", expected)
	require.NoError(t, err)
	require.True(t, cleared)
	limits := loadLimits()
	require.NotContains(t, limits, "AICredits")
	require.Equal(t, concurrentLimit, limits["claude-sonnet-4-6"])
	require.Equal(t, before+1, countOutbox(), "key removal and scheduler invalidation must commit together")

	// A new generation of AICredits must survive a late successful request
	// trying to remove the previously observed generation.
	require.NoError(t, repo.SetModelRateLimit(ctx, account.ID, "AICredits", time.Now().Add(-time.Minute)))
	expected = loadLimits()["AICredits"].(map[string]any)
	require.NoError(t, repo.SetModelRateLimit(ctx, account.ID, "AICredits", time.Now().Add(5*time.Hour)))
	newCredits := loadLimits()["AICredits"]
	before = countOutbox()
	cleared, err = repo.ClearModelRateLimitIfUnchanged(ctx, account.ID, "AICredits", expected)
	require.NoError(t, err)
	require.False(t, cleared)
	limits = loadLimits()
	require.Equal(t, newCredits, limits["AICredits"])
	require.Equal(t, concurrentLimit, limits["claude-sonnet-4-6"])
	require.Equal(t, before, countOutbox(), "stale clears must not publish scheduler events")
}
