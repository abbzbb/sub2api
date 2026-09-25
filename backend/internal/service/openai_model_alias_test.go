package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeKnownOpenAICodexModelGPT6Identity(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "gpt-6", want: "gpt-6-astra"},
		{input: "gpt-6-astra", want: "gpt-6-astra"},
		{input: "gpt-6-sol", want: "gpt-6-sol"},
		{input: "gpt-6-sol-max", want: "gpt-6-sol"},
		{input: "gpt-6-luna", want: "gpt-6-luna"},
		{input: "openai/gpt-6-sol", want: "gpt-6-sol"},
		{input: "openai/gpt-6-luna", want: "gpt-6-luna"},
		{input: "gpt-5.6", want: "gpt-5.6-sol"},
		// Prefix collisions must not be claimed as Sol/Luna.
		{input: "gpt-6-solitude", want: ""},
		{input: "gpt-6-sol-preview", want: ""},
		{input: "gpt-6-luna-preview", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			require.Equal(t, tt.want, normalizeKnownOpenAICodexModel(tt.input))
		})
	}
}

func TestNormalizeKnownOpenAICodexModelGPT6Astra(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "openai/gpt-6-astra", "OPENAI/GPT-6_ASTRA", "gpt-6", "openai/gpt-6"} {
		require.Equal(t, "gpt-6-astra", normalizeKnownOpenAICodexModel(model))
	}
}

func TestNormalizeKnownOpenAICodexModel_BareGPT56RoutesToSol(t *testing.T) {
	tests := map[string]string{
		"gpt-5.6":            "gpt-5.6-sol",
		"openai/gpt-5.6":     "gpt-5.6-sol",
		"gpt5.6":             "gpt-5.6-sol",
		"gpt-5.6-high":       "gpt-5.6-sol",
		"gpt-5.6-max":        "gpt-5.6-sol",
		"gpt-5.6-2026-07-09": "gpt-5.6-sol",
		"openai/gpt-5.6-max": "gpt-5.6-sol",
	}

	for input, expected := range tests {
		t.Run(input, func(t *testing.T) {
			require.Equal(t, expected, normalizeKnownOpenAICodexModel(input))
		})
	}
}

func TestUsageBillingModelCandidates_BareGPT56IncludesSol(t *testing.T) {
	require.Equal(t,
		[]string{"gpt-5.6", "gpt-5.6-sol"},
		usageBillingModelCandidates("gpt-5.6"),
	)
	require.Equal(t,
		[]string{"openai/gpt-5.6", "gpt-5.6", "gpt-5.6-sol"},
		usageBillingModelCandidates("openai/gpt-5.6"),
	)
}
