//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBatchImageProviders_ApplyAccountModelMapping(t *testing.T) {
	for _, tc := range []struct {
		name    string
		model   string
		mapping map[string]any
		want    string
		fromJob bool
	}{
		{name: "exact alias", model: "public-image", mapping: map[string]any{"public-image": "gemini-2.5-flash-image"}, want: "gemini-2.5-flash-image"},
		{name: "wildcard alias", model: "public-image", mapping: map[string]any{"public-*": "gemini-2.5-flash-image"}, want: "gemini-2.5-flash-image"},
		{name: "unmapped model", model: "gemini-2.5-flash-image", want: "gemini-2.5-flash-image"},
		{name: "job model fallback", model: "public-image", mapping: map[string]any{"public-image": "gemini-2.5-flash-image"}, want: "gemini-2.5-flash-image", fromJob: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := &BatchImageJob{BatchID: "imgbatch_mapping", Model: tc.model}
			input := validGeminiBatchInput()
			input.BatchID = job.BatchID
			input.Model = tc.model
			if tc.fromJob {
				input.Model = ""
			}
			t.Run("gemini", func(t *testing.T) {
				client := &fakeGeminiBatchClient{}
				account := geminiAPIKeyAccount("test-key")
				account.Credentials["model_mapping"] = tc.mapping
				_, err := NewGeminiAPIBatchImageProvider(client).Submit(context.Background(), job, account, input)
				require.NoError(t, err)
				require.Equal(t, tc.want, client.createdModel)
			})
			t.Run("vertex", func(t *testing.T) {
				client := &fakeVertexBatchClient{}
				account := vertexServiceAccount()
				account.Credentials["model_mapping"] = tc.mapping
				_, err := newTestVertexProvider(client, &fakeVertexObjectStore{}).Submit(context.Background(), job, account, input)
				require.NoError(t, err)
				require.Equal(t, NormalizeVertexBatchModelPath(tc.want), client.createdReq.Model)
			})
			// Public job metadata remains suitable for display and billing.
			require.Equal(t, tc.model, job.Model)
		})
	}
}
