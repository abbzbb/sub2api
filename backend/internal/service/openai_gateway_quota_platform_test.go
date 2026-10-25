package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIRecordUsageQuotaPlatformFallback(t *testing.T) {
	for _, tc := range []struct {
		name     string
		group    string
		account  string
		resolved string
		want     string
	}{
		{"composite OpenAI", PlatformComposite, PlatformOpenAI, "", PlatformOpenAI},
		{"composite Grok", PlatformComposite, PlatformGrok, "", PlatformGrok},
		{"concrete group preserved", PlatformOpenAI, PlatformGrok, "", PlatformOpenAI},
		{"resolved platform preserved", PlatformComposite, PlatformGrok, PlatformGemini, PlatformGemini},
	} {
		t.Run(tc.name, func(t *testing.T) {
			billing := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: false}}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(
				&openAIRecordUsageLogRepoStub{inserted: true}, billing,
				&openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil,
			)
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "quota-platform", Model: "gpt-5.1",
					Usage: OpenAIUsage{InputTokens: 1200, OutputTokens: 300}},
				APIKey: &APIKey{ID: 2, Group: &Group{Platform: tc.group}},
				User:   &User{ID: 1}, Account: &Account{ID: 3, Type: AccountTypeAPIKey, Platform: tc.account},
				QuotaPlatform: tc.resolved,
			})
			require.NoError(t, err)
			require.Equal(t, 1, billing.calls)
			require.NotNil(t, billing.lastCmd)
			require.Positive(t, billing.lastCmd.BalanceCost)
			require.Equal(t, tc.want, billing.lastCmd.PlatformQuotaPlatform)
		})
	}
}
