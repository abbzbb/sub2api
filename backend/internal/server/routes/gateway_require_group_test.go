package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newUngroupedGatewayRoutesTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	settingService := service.NewSettingService(&channelMonitorRouteSettingRepoStub{
		values: map[string]string{
			service.SettingKeyAllowUngroupedKeyScheduling: "false",
		},
	}, &config.Config{})
	RegisterGatewayRoutes(
		router,
		&handler.Handlers{
			Gateway:       &handler.GatewayHandler{},
			OpenAIGateway: &handler.OpenAIGatewayHandler{},
			AsyncImage:    handler.NewAsyncImageHandler(nil, nil),
		},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
				Key:    "ungrouped-key",
				Status: service.StatusActive,
			})
			c.Next()
		}),
		nil,
		nil,
		nil,
		settingService,
		nil,
		&config.Config{
			Gateway: config.GatewayConfig{
				MaxBodySize:     1024 * 1024,
				TextMaxBodySize: 1024 * 1024,
			},
		},
		nil,
	)
	return router
}

func TestGatewayRootAndCodexUngroupedKeyUsesOpenAIEnvelope(t *testing.T) {
	router := newUngroupedGatewayRoutesTestRouter()

	openaiPaths := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/responses", `{"model":"gpt-5"}`},
		{http.MethodPost, "/chat/completions", `{"model":"gpt-5"}`},
		{http.MethodPost, "/backend-api/codex/responses", `{"model":"gpt-5"}`},
	}
	for _, tc := range openaiPaths {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusForbidden, w.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.NotContains(t, body, "type", "OpenAI envelope must not include top-level type")
			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "permission_error", errObj["type"])
			require.Contains(t, errObj["message"], "not assigned to any group")
		})
	}

	t.Run("anthropic messages keeps type error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4","messages":[]}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusForbidden, w.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "error", body["type"])
		errObj, ok := body["error"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "permission_error", errObj["type"])
		require.Contains(t, errObj["message"], "not assigned to any group")
	})
}
