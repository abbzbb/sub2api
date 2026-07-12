package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeImageGenerationCallStatusInSSEData_OutputItemDone(t *testing.T) {
	raw := []byte(`{"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","status":"generating","result":"iVBORw0KGgo"}}`)
	normalized, ok := normalizeImageGenerationCallStatusInSSEData(raw)
	require.True(t, ok)
	require.Equal(t, "completed", gjson.GetBytes(normalized, "item.status").String())
	require.Equal(t, "iVBORw0KGgo", gjson.GetBytes(normalized, "item.result").String())
}

func TestNormalizeImageGenerationCallStatusInSSEData_TerminalResponse(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[{"id":"ig_1","type":"image_generation_call","status":"generating","result":"png-data"},{"id":"msg_1","type":"message","status":"completed"}]}}`)
	normalized, ok := normalizeImageGenerationCallStatusInSSEData(raw)
	require.True(t, ok)
	require.Equal(t, "completed", gjson.GetBytes(normalized, "response.output.0.status").String())
	// Non-image items must remain untouched.
	require.Equal(t, "completed", gjson.GetBytes(normalized, "response.output.1.status").String())
	require.Equal(t, "message", gjson.GetBytes(normalized, "response.output.1.type").String())
}

func TestNormalizeImageGenerationCallStatusInSSEData_NoResultKeepsStatus(t *testing.T) {
	raw := []byte(`{"type":"response.output_item.done","item":{"id":"ig_1","type":"image_generation_call","status":"generating"}}`)
	normalized, ok := normalizeImageGenerationCallStatusInSSEData(raw)
	require.False(t, ok)
	require.Equal(t, string(raw), string(normalized))
}

func TestExtractImageGenerationOutputFromSSEData_ForcesCompleted(t *testing.T) {
	raw := []byte(`{"type":"response.output_item.done","item":{"id":"ig_2","type":"image_generation_call","status":"generating","result":"png"}}`)
	item, ok := extractImageGenerationOutputFromSSEData(raw, map[string]struct{}{})
	require.True(t, ok)
	require.Equal(t, "completed", gjson.GetBytes(item, "status").String())
	require.Equal(t, "png", gjson.GetBytes(item, "result").String())
}

func TestNormalizeResponsesStreamingTerminalOutput_NormalizesExistingImageStatus(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[{"id":"ig_1","type":"image_generation_call","status":"generating","result":"png-data"}],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	normalized, ok := normalizeResponsesStreamingTerminalOutput(raw, nil, nil)
	require.True(t, ok)
	require.Equal(t, "completed", gjson.GetBytes(normalized, "response.output.0.status").String())
	require.Equal(t, "png-data", gjson.GetBytes(normalized, "response.output.0.result").String())
}
