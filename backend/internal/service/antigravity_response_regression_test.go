package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func antigravityRegressionContext() (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	return c, recorder
}

func antigravityRegressionResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestAntigravityNonStreamingRetainsTerminalMetadata(t *testing.T) {
	for _, splitUsage := range []bool{false, true} {
		name := "combined_terminal_chunk"
		terminal := `data: {"response":{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":50,"thoughtsTokenCount":20,"cachedContentTokenCount":10}}}` + "\n\n"
		if splitUsage {
			name = "usage_after_finish_reason"
			terminal = `data: {"response":{"candidates":[{"finishReason":"MAX_TOKENS"}]}}` + "\n\n" +
				`data: {"response":{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":50,"thoughtsTokenCount":20,"cachedContentTokenCount":10}}}` + "\n\n"
		}
		t.Run(name, func(t *testing.T) {
			body := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"answer"}]}}]}}` + "\n\n" + terminal
			for _, protocol := range []string{"claude", "chat", "responses", "gemini"} {
				t.Run(protocol, func(t *testing.T) {
					svc := newAntigravityTestService(&config.Config{})
					c, recorder := antigravityRegressionContext()
					resp := antigravityRegressionResponse(body)
					var result *antigravityStreamResult
					var err error
					switch protocol {
					case "claude":
						result, err = svc.handleClaudeStreamToNonStreaming(c, resp, time.Now(), "claude-sonnet-4-6")
					case "chat":
						result, err = svc.handleChatCompletionsNonStreamingFromAntigravity(c, resp, time.Now(), "claude-sonnet-4-6")
					case "responses":
						result, err = svc.handleResponsesNonStreamingFromAntigravity(c, resp, time.Now(), "claude-sonnet-4-6")
					case "gemini":
						result, err = svc.handleGeminiStreamToNonStreaming(c, resp, time.Now())
					}
					require.NoError(t, err)
					require.Equal(t, 90, result.usage.InputTokens)
					require.Equal(t, 70, result.usage.OutputTokens)
					require.Equal(t, 10, result.usage.CacheReadInputTokens)
					require.Contains(t, recorder.Body.String(), "answer")
					if protocol == "claude" {
						require.Equal(t, "max_tokens", gjson.GetBytes(recorder.Body.Bytes(), "stop_reason").String())
						require.Equal(t, int64(70), gjson.GetBytes(recorder.Body.Bytes(), "usage.output_tokens").Int())
					}
					if protocol == "gemini" {
						require.Equal(t, "MAX_TOKENS", gjson.GetBytes(recorder.Body.Bytes(), "candidates.0.finishReason").String())
						require.Equal(t, int64(50), gjson.GetBytes(recorder.Body.Bytes(), "usageMetadata.candidatesTokenCount").Int())
					}
				})
			}
		})
	}
}

func TestAntigravityStreamErrorsDoNotEmitSuccessfulCompletion(t *testing.T) {
	prefix := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"partial answer"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}}` + "\n\n"
	for _, partial := range []bool{false, true} {
		for _, wrapped := range []bool{false, true} {
			for _, protocol := range []string{"claude", "chat", "responses", "gemini_nonstream", "claude_nonstream"} {
				t.Run(protocol+"/partial="+stringBool(partial)+"/wrapped="+stringBool(wrapped), func(t *testing.T) {
					payload := `{"error":{"code":503,"status":"UNAVAILABLE","message":"error for projects/123456789012/serviceAccounts/test@example.com"}}`
					if wrapped {
						payload = `{"response":` + payload + `}`
					}
					body := "event: error\ndata: " + payload + "\n\n"
					if partial {
						body = prefix + body
					}
					svc := newAntigravityTestService(&config.Config{})
					c, recorder := antigravityRegressionContext()
					resp := antigravityRegressionResponse(body)
					var result *antigravityStreamResult
					var err error
					switch protocol {
					case "claude":
						result, err = svc.handleClaudeStreamingResponse(c, resp, time.Now(), "claude-sonnet-4-6")
					case "chat":
						result, err = svc.handleChatCompletionsStreamingFromAntigravity(c, resp, time.Now(), "claude-sonnet-4-6", true)
					case "responses":
						result, err = svc.handleResponsesStreamingFromAntigravity(c, resp, time.Now(), "claude-sonnet-4-6")
					case "gemini_nonstream":
						result, err = svc.handleGeminiStreamToNonStreaming(c, resp, time.Now())
					case "claude_nonstream":
						_, result, err = svc.collectClaudeStreamResponse(c, resp, time.Now(), "claude-sonnet-4-6")
					}
					require.Error(t, err)
					require.NotContains(t, recorder.Body.String(), "test@example.com")
					buffered := strings.HasSuffix(protocol, "nonstream")
					if !partial || buffered {
						var failover *UpstreamFailoverError
						require.ErrorAs(t, err, &failover)
						require.Equal(t, http.StatusServiceUnavailable, failover.StatusCode)
						require.False(t, c.Writer.Written())
					} else {
						require.NotNil(t, result)
						require.Equal(t, 2, result.usage.OutputTokens)
						require.Contains(t, recorder.Body.String(), "Upstream generation failed")
						require.NotContains(t, recorder.Body.String(), "message_stop")
						require.NotContains(t, recorder.Body.String(), "[DONE]")
						require.NotContains(t, recorder.Body.String(), "response.completed")
						streamError, ok := GetOpsStreamError(c)
						require.True(t, ok, "HTTP 200 must still be recorded as an upstream failure")
						require.True(t, streamError.CountTowardsSLA)
						require.Equal(t, http.StatusServiceUnavailable, streamError.IntendedStatus)
					}
				})
			}
		}
	}
}

func stringBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestAntigravityThinkingAliasHonorsActualUpstreamCooldown(t *testing.T) {
	ctx := WithThinkingEnabled(context.Background(), true, false)
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{}, Extra: map[string]any{
		modelRateLimitsKey: map[string]any{"claude-sonnet-4-6": map[string]any{
			"rate_limit_reset_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}},
	}}
	require.Equal(t, "claude-sonnet-4-6", resolveFinalAntigravityModelKey(ctx, account, "claude-sonnet-4-5"))
	require.True(t, account.isModelRateLimitedWithContext(ctx, "claude-sonnet-4-5"))
	require.False(t, account.isModelRateLimitedWithContext(context.Background(), "claude-sonnet-4-5"))
}

func TestAntigravityForwardRetainsUsageOnTerminalStreamError(t *testing.T) {
	stream := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"partial answer"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}}` + "\n\n" +
		`data: {"error":{"code":503,"status":"UNAVAILABLE","message":"generation failed"}}` + "\n\n"
	for _, protocol := range []string{"claude", "chat", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityRegressionResponse(stream)}}
			svc := newAntigravityCompatService(config.GatewayConfig{}, upstream)
			c, recorder := antigravityRegressionContext()
			account := newAntigravityCompatAccount(AccountTypeOAuth)
			var result *ForwardResult
			var err error
			switch protocol {
			case "claude":
				result, err = svc.Forward(context.Background(), c, account, []byte(`{"model":"claude-sonnet-4-6","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`), false)
			case "chat":
				result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(`{"model":"claude-sonnet-4-6","stream":true,"messages":[{"role":"user","content":"hello"}]}`), nil)
			case "responses":
				result, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(`{"model":"claude-sonnet-4-6","stream":true,"input":"hello"}`), nil)
			}
			require.Error(t, err)
			require.NotNil(t, result)
			require.Equal(t, 3, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
			require.True(t, result.Stream)
			require.Equal(t, "claude-sonnet-4-6", result.UpstreamModel)
			require.Contains(t, recorder.Body.String(), "Upstream generation failed")
			require.NotContains(t, recorder.Body.String(), "message_stop")
			require.NotContains(t, recorder.Body.String(), "[DONE]")
			require.NotContains(t, recorder.Body.String(), "response.completed")
		})
	}
}
