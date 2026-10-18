package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type atomicCreditsClearRepo struct {
	AccountRepository
	limits     map[string]any
	clearCalls int
	clearErr   error
}

func (r *atomicCreditsClearRepo) ClearModelRateLimitIfUnchanged(_ context.Context, _ int64, model string, expected map[string]any) (bool, error) {
	r.clearCalls++
	if r.clearErr != nil {
		return false, r.clearErr
	}
	if !reflect.DeepEqual(r.limits[model], expected) {
		return false, nil
	}
	delete(r.limits, model)
	return true, nil
}

func (r *atomicCreditsClearRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	panic("clearing credits must not replace the whole model_rate_limits map")
}

func TestAntigravityCreditsClearPreservesConcurrentCooldowns(t *testing.T) {
	oldCredits := map[string]any{"rate_limit_reset_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}
	newCredits := map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	for _, outcome := range []string{"clear", "new_generation", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			localLimits := map[string]any{creditsExhaustedKey: oldCredits}
			account := &Account{ID: 42, Extra: map[string]any{modelRateLimitsKey: localLimits}}
			// A different request persisted this model cooldown after our snapshot.
			concurrent := map[string]any{"rate_limit_reset_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
			repo := &atomicCreditsClearRepo{limits: map[string]any{creditsExhaustedKey: oldCredits, "claude-sonnet-4-6": concurrent}}
			if outcome == "new_generation" {
				repo.limits[creditsExhaustedKey] = newCredits
			}
			if outcome == "failure" {
				repo.clearErr = errors.New("database unavailable")
			}
			svc := &AntigravityGatewayService{accountRepo: repo}
			svc.clearCreditsExhausted(context.Background(), account)
			require.Equal(t, 1, repo.clearCalls)
			require.Equal(t, concurrent, repo.limits["claude-sonnet-4-6"])
			if outcome == "clear" {
				require.NotContains(t, repo.limits, creditsExhaustedKey)
				require.NotContains(t, account.Extra[modelRateLimitsKey], creditsExhaustedKey)
				require.Contains(t, localLimits, creditsExhaustedKey, "do not mutate another reference to the original snapshot")
			} else {
				require.Contains(t, repo.limits, creditsExhaustedKey)
				require.Contains(t, account.Extra[modelRateLimitsKey], creditsExhaustedKey)
			}
		})
	}
}
