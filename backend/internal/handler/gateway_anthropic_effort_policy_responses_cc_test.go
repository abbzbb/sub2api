package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Anthropic-platform /v1/responses and /v1/chat/completions must enforce the
// same group reasoning-effort ceiling as /v1/messages. Deny mode short-circuits
// before account selection so the handler wiring is observable without a full
// upstream forward.
func TestGatewayHandlerResponsesAndChatCompletions_AnthropicEffortPolicyDeny(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(9101)
	userID := int64(9102)
	apiKey := &service.APIKey{
		ID:      9103,
		GroupID: &groupID,
		Group: &service.Group{
			ID:                           groupID,
			Platform:                     service.PlatformAnthropic,
			MaxReasoningEffort:           "high",
			MaxReasoningEffortOverLimit:  service.ReasoningEffortOverLimitDeny,
		},
		User: &service.User{ID: userID, Status: service.StatusActive},
	}

	for _, tc := range []struct {
		name string
		path string
		body string
		call func(*GatewayHandler, *gin.Context)
	}{
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"claude-opus-4-6","input":"hi","reasoning":{"effort":"max"}}`,
			call: (*GatewayHandler).Responses,
		},
		{
			name: "chat_completions",
			path: "/v1/chat/completions",
			body: `{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"max"}`,
			call: (*GatewayHandler).ChatCompletions,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID, Concurrency: 1})

			tc.call(&GatewayHandler{}, c)

			require.Equal(t, http.StatusForbidden, rec.Code)
			body := rec.Body.String()
			require.Contains(t, body, "permission_error")
			require.Contains(t, gjson.Get(body, "error.message").String(), "high")
			_, selected := c.Get(opsAccountIDKey)
			require.False(t, selected, "deny must happen before account selection")
		})
	}
}
