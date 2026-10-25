//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAICompositeBillingUsesAccountPlatformAndDeduplicates(t *testing.T) {
	for _, mode := range []string{"tokens", "cyber", "grok-video"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			repo, seed, _, client := durableBillingFixture(t)
			cfg := &config.Config{}
			cfg.Default.RateMultiplier = 1
			svc := service.NewOpenAIGatewayService(
				nil, nil, NewUsageLogRepository(client, repo.db), repo, nil, nil, nil, nil, cfg,
				nil, nil, service.NewBillingService(cfg, nil), nil, nil, nil, nil,
				nil, nil, nil, nil, nil, nil, nil,
			)
			user := &service.User{ID: seed.UserID, Balance: 100}
			key := &service.APIKey{ID: seed.APIKeyID, UserID: user.ID, User: user,
				Group: &service.Group{Platform: service.PlatformComposite, RateMultiplier: 1}}
			account := &service.Account{ID: seed.AccountID, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}
			result := &service.OpenAIForwardResult{RequestID: seed.RequestID, Model: "gpt-5.1", Duration: time.Second,
				Usage: service.OpenAIUsage{InputTokens: 1200, OutputTokens: 300}}
			if mode == "grok-video" {
				account.Platform = service.PlatformGrok
				result = &service.OpenAIForwardResult{
					RequestID: "grok-video:" + seed.RequestID, Model: "grok-imagine-video",
					VideoCount: 1, VideoDurationSeconds: 6, VideoResolution: "720p",
				}
			}
			require.Empty(t, service.QuotaPlatform(ctx, key), "status/background contexts cannot resolve a composite group")
			for attempt := 0; attempt < 2; attempt++ {
				if mode == "cyber" {
					svc.RecordCyberPolicyUsageLog(ctx, service.CyberPolicyUsageInput{
						APIKey: key, Account: account, RequestID: result.RequestID, Model: result.Model,
						InputTokens: 1200, OutputTokens: 300, Stream: true,
					})
				} else {
					require.NoError(t, svc.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
						Result: result, APIKey: key, User: user, Account: account,
					}))
				}
			}
			var state, platform string
			var cost, balance float64
			require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT state,payload->'command'->>'PlatformQuotaPlatform',
				(payload->'command'->>'BalanceCost')::numeric FROM usage_billing_events WHERE request_id=$1 AND api_key_id=$2`, result.RequestID, key.ID).Scan(&state, &platform, &cost))
			require.Equal(t, "complete", state)
			require.Equal(t, account.Platform, platform)
			require.Positive(t, cost)
			require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, user.ID).Scan(&balance))
			require.InDelta(t, 100-cost, balance, 1e-8, "two deliveries debit once")
			var logs, claims int
			require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM usage_logs WHERE request_id=$1 AND api_key_id=$2`, result.RequestID, key.ID).Scan(&logs))
			require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2`, result.RequestID, key.ID).Scan(&claims))
			require.Equal(t, 1, logs)
			require.Equal(t, 1, claims)
		})
	}
}
