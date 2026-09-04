//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGrokAmbiguousFailureTrackerTripsOnlyOnRepeatedFailuresInWindow(t *testing.T) {
	var tr grokAmbiguousFailureTracker
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Two hits inside the window: below threshold, no cooldown.
	for i := 0; i < grokAmbiguousFailureThreshold-1; i++ {
		cooldown, tripped := tr.record(1, base.Add(time.Duration(i)*time.Second))
		require.False(t, tripped)
		require.Zero(t, cooldown)
	}

	// Window rolls over before the third hit: counter restarts, still no cooldown.
	_, tripped := tr.record(1, base.Add(grokAmbiguousFailureWindow+time.Second))
	require.False(t, tripped)

	// Threshold hits in one window trip the first rung of the ladder.
	now := base.Add(10 * time.Minute)
	var got time.Duration
	for i := 0; i < grokAmbiguousFailureThreshold; i++ {
		got, tripped = tr.record(1, now.Add(time.Duration(i)*time.Second))
	}
	require.True(t, tripped)
	require.Equal(t, grokAmbiguousCooldownLadder[0], got)

	// Another burst shortly after escalates to the next rung, then caps.
	for level := 1; level < len(grokAmbiguousCooldownLadder)+1; level++ {
		now = now.Add(grokAmbiguousCooldownLadder[min(level-1, len(grokAmbiguousCooldownLadder)-1)] + time.Second)
		for i := 0; i < grokAmbiguousFailureThreshold; i++ {
			got, tripped = tr.record(1, now.Add(time.Duration(i)*time.Second))
		}
		require.True(t, tripped)
		want := grokAmbiguousCooldownLadder[min(level, len(grokAmbiguousCooldownLadder)-1)]
		require.Equal(t, want, got, "level %d", level)
	}

	// Quiet for longer than the decay period after the last cooldown ended:
	// escalation level resets.
	now = now.Add(grokAmbiguousCooldownLadder[len(grokAmbiguousCooldownLadder)-1] + grokAmbiguousFailureDecay + time.Minute)
	for i := 0; i < grokAmbiguousFailureThreshold; i++ {
		got, tripped = tr.record(1, now.Add(time.Duration(i)*time.Second))
	}
	require.True(t, tripped)
	require.Equal(t, grokAmbiguousCooldownLadder[0], got)

	// Accounts are tracked independently.
	_, tripped = tr.record(2, now)
	require.False(t, tripped)
}

func TestGrokAmbiguousFailureTrackerResetClearsHistory(t *testing.T) {
	var tr grokAmbiguousFailureTracker
	now := time.Now()
	for i := 0; i < grokAmbiguousFailureThreshold-1; i++ {
		_, tripped := tr.record(7, now)
		require.False(t, tripped)
	}
	tr.reset(7)
	_, tripped := tr.record(7, now)
	require.False(t, tripped, "reset must discard the partial window")
}

func TestHandleGrokAccountUpstreamErrorAmbiguous403EscalatesToRuntimeOnlyCooldown(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			account := &Account{ID: 623, Platform: PlatformGrok, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
			repo := &grokQuotaAccountRepo{}
			svc := &OpenAIGatewayService{accountRepo: repo}
			body := []byte(`{"error":{"code":"permission_denied","message":"permission denied"}}`)

			for i := 0; i < grokAmbiguousFailureThreshold-1; i++ {
				require.False(t, svc.handleGrokAccountUpstreamError(context.Background(), account, status, nil, body))
				require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "hit %d must not block", i+1)
			}

			require.False(t, svc.handleGrokAccountUpstreamError(context.Background(), account, status, nil, body))
			require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "threshold hit must install a runtime block")

			// Runtime only: no durable writes of any kind.
			require.Zero(t, repo.errorCalls)
			require.Zero(t, repo.credentialErrorCalls)
			require.Zero(t, repo.tempUnschedCalls)
			require.Zero(t, repo.rateLimitedCalls)
			require.Empty(t, repo.modelRateLimitCalls)
			require.Nil(t, account.TempUnschedulableUntil)
		})
	}
}

func TestHandleGrokAccountUpstreamErrorAmbiguousSuccessResetsCounter(t *testing.T) {
	account := &Account{ID: 624, Platform: PlatformGrok, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	body := []byte(`{"error":{"code":"permission_denied","message":"permission denied"}}`)

	for i := 0; i < grokAmbiguousFailureThreshold-1; i++ {
		svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body)
	}
	svc.updateGrokUsageFromResponse(context.Background(), account, http.Header{}, http.StatusOK)

	for i := 0; i < grokAmbiguousFailureThreshold-1; i++ {
		svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body)
	}
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "a success in between must restart the window")
}

func TestHandleGrokAccountUpstreamErrorAmbiguousCooldownRespectsPoolMode(t *testing.T) {
	// Pool accounts share the same failure signal; the runtime-only block still
	// applies because it is short, escalating and never persisted.
	account := &Account{
		ID: 625, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "k", "pool_mode": true},
	}
	require.True(t, account.IsPoolMode())
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	body := []byte(`{"error":{"code":"permission_denied","message":"permission denied"}}`)
	for i := 0; i < grokAmbiguousFailureThreshold; i++ {
		svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusForbidden, nil, body)
	}
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Zero(t, repo.tempUnschedCalls)
}
