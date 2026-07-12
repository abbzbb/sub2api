package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// #3687: non-Codex /v1/responses must not inject Codex base instructions
// (fixed-size cache input from default system prompt).
func TestForwardResponses_NonCodexDoesNotInjectDefaultInstructions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false

	newUpstream := func() *httpUpstreamRecorder {
		return &httpUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"usage":{"input_tokens":1,"output_tokens":2}}`)),
			},
		}
	}

	t.Run("APIKey", func(t *testing.T) {
		upstream := newUpstream()
		svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
		account := &Account{
			ID: 5011, Name: "apikey", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://example.com"},
			Extra:       map[string]any{"use_responses_api": true},
		}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
		c.Request.Header.Set("User-Agent", "custom-client/1.0")
		SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

		body := []byte(`{"model":"gpt-5.4","stream":false,"input":"hello"}`)
		_, err := svc.Forward(context.Background(), c, account, body)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(upstream.lastBody, "instructions").Exists())
		require.NotContains(t, string(upstream.lastBody), "You are Codex")
		require.NotContains(t, string(upstream.lastBody), "running in the Codex CLI")
	})

	t.Run("OAuth", func(t *testing.T) {
		upstream := newUpstream()
		svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
		account := &Account{
			ID: 5012, Name: "oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
			Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "acc"},
			Status:      StatusActive, Schedulable: true,
		}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
		c.Request.Header.Set("User-Agent", "custom-client/1.0")
		SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

		// Match other OAuth unit fixtures: omit stream; transform forces stream=true.
		body := []byte(`{"model":"gpt-5.4","input":"hello"}`)
		_, err := svc.Forward(context.Background(), c, account, body)
		require.NoError(t, err)
		// OAuth requires the field present, but value must stay empty (no base prompt).
		require.True(t, gjson.GetBytes(upstream.lastBody, "instructions").Exists())
		require.Equal(t, "", gjson.GetBytes(upstream.lastBody, "instructions").String())
		require.NotContains(t, string(upstream.lastBody), "You are Codex")
		require.NotContains(t, string(upstream.lastBody), "running in the Codex CLI")
	})
}
