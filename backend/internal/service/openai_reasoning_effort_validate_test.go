package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateOpenAIResponsesReasoningEffort_AllowsKnownValues(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"model":"gpt-5.5"}`,
		`{"reasoning":{"effort":"none"}}`,
		`{"reasoning":{"effort":"minimal"}}`,
		`{"reasoning":{"effort":"low"}}`,
		`{"reasoning":{"effort":"medium"}}`,
		`{"reasoning":{"effort":"high"}}`,
		`{"reasoning":{"effort":"xhigh"}}`,
		`{"reasoning":{"effort":"x-high"}}`,
		`{"reasoning":{"effort":"max"}}`,
		`{"reasoning":{"effort":"ultra"}}`,
		`{"reasoning":{"effort":"MAX"}}`,
		`{"reasoning_effort":"high"}`,
		`{"reasoning":{"effort":""}}`,
	} {
		require.NoError(t, ValidateOpenAIResponsesReasoningEffort([]byte(body)), body)
	}
}

func TestValidateOpenAIResponsesReasoningEffort_RejectsTypos(t *testing.T) {
	err := ValidateOpenAIResponsesReasoningEffort([]byte(`{"reasoning":{"effort":"xhign"}}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "xhign")
	require.True(t, strings.Contains(err.Error(), "Supported values"))
}

func TestValidateOpenAIResponsesReasoningEffort_RejectsNonString(t *testing.T) {
	err := ValidateOpenAIResponsesReasoningEffort([]byte(`{"reasoning":{"effort":123}}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected a string")
}
