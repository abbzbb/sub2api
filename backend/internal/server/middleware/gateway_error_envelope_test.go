//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayProtocolErrorWriterSelectsEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name          string
		path          string
		wantAnthropic bool
		wantGoogle    bool
		wantOpenAI    bool
	}{
		{name: "completions", path: "/v1/chat/completions", wantOpenAI: true},
		{name: "responses", path: "/v1/responses", wantOpenAI: true},
		{name: "anthropic", path: "/v1/messages", wantAnthropic: true},
		{name: "gemini", path: "/v1beta/models/gemini-pro:generateContent", wantGoogle: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			GatewayProtocolErrorWriter(c, http.StatusForbidden, "API Key is not assigned to any group")

			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			switch {
			case tc.wantAnthropic:
				require.Equal(t, "error", body["type"])
				errObj, _ := body["error"].(map[string]any)
				require.Equal(t, "permission_error", errObj["type"])
			case tc.wantGoogle:
				errObj, _ := body["error"].(map[string]any)
				require.EqualValues(t, http.StatusForbidden, errObj["code"])
				require.NotEmpty(t, errObj["status"])
			case tc.wantOpenAI:
				errObj, _ := body["error"].(map[string]any)
				require.Equal(t, "permission_error", errObj["type"])
				require.Equal(t, "API Key is not assigned to any group", errObj["message"])
				require.NotContains(t, body, "type")
			}
		})
	}
}

func TestAPIKeyAuthGatewayMissingAndInvalidKeyEnvelopes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
		return nil, service.ErrAPIKeyNotFound
	}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	apiKeyService := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, cfg)))
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	router.POST("/v1/messages", ok)
	router.POST("/v1/chat/completions", ok)
	router.POST("/v1/responses", ok)

	t.Run("missing_key_anthropic", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code)

		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "error", body["type"])
		errObj, _ := body["error"].(map[string]any)
		require.Equal(t, "authentication_error", errObj["type"])
		require.Equal(t, "API_KEY_REQUIRED", errObj["code"])
		require.Contains(t, errObj["message"], "API key is required")
		require.NotContains(t, w.Body.String(), `"code":401`)
	})

	t.Run("missing_key_openai_completions", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code)

		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		errObj, _ := body["error"].(map[string]any)
		require.Equal(t, "invalid_request_error", errObj["type"])
		require.Equal(t, "API_KEY_REQUIRED", errObj["code"])
		require.NotContains(t, body, "type")
		require.NotContains(t, w.Body.String(), `"code":401`)
	})

	t.Run("bad_key_anthropic", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("x-api-key", "not-a-real-key")
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code)

		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Equal(t, "error", body["type"])
		errObj, _ := body["error"].(map[string]any)
		require.Equal(t, "authentication_error", errObj["type"])
		require.Equal(t, "INVALID_API_KEY", errObj["code"])
		require.Equal(t, "Invalid API key", errObj["message"])
	})

	t.Run("bad_key_openai_responses", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req.Header.Set("Authorization", "Bearer not-a-real-key")
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code)

		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		errObj, _ := body["error"].(map[string]any)
		require.Equal(t, "INVALID_API_KEY", errObj["code"])
		require.Equal(t, "Invalid API key", errObj["message"])
		require.NotContains(t, body, "type")
	})
}

func TestRecoveryGatewayPanicUsesProtocolEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name          string
		path          string
		wantAnthropic bool
		wantOpenAI    bool
	}{
		{name: "messages", path: "/v1/messages", wantAnthropic: true},
		{name: "completions", path: "/v1/chat/completions", wantOpenAI: true},
		{name: "responses", path: "/v1/responses", wantOpenAI: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(Recovery())
			r.POST(tc.path, func(c *gin.Context) { panic("boom") })

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusInternalServerError, w.Code)
			require.NotContains(t, w.Body.String(), `"code":500`, "must not emit panel {code:int}")

			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			switch {
			case tc.wantAnthropic:
				require.Equal(t, "error", body["type"])
				errObj, _ := body["error"].(map[string]any)
				require.Equal(t, "api_error", errObj["type"])
				require.Equal(t, infraerrors.UnknownMessage, errObj["message"])
			case tc.wantOpenAI:
				errObj, _ := body["error"].(map[string]any)
				require.Equal(t, "api_error", errObj["type"])
				require.Equal(t, infraerrors.UnknownMessage, errObj["message"])
				require.NotContains(t, body, "type")
			}
		})
	}
}

func TestRecoveryPanelPathKeepsIntCodeEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(Recovery())
	r.GET("/api/v1/users/me", func(c *gin.Context) { panic("boom") })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	var got response.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, http.StatusInternalServerError, got.Code)
	require.Equal(t, infraerrors.UnknownMessage, got.Message)
}

func TestAbortWithErrorPanelPathKeepsStringCode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
	AbortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "Authorization required")

	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "UNAUTHORIZED", resp.Code)
	require.Equal(t, "Authorization required", resp.Message)
}

func TestRequireGroupAssignmentUsesProtocolEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settingService := service.NewSettingService(fakeSettingRepo{
		values: map[string]string{
			service.SettingKeyAllowUngroupedKeyScheduling: "false",
		},
	}, &config.Config{})
	apiKey := &service.APIKey{ID: 100, Key: "ungrouped-key", Status: service.StatusActive}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), apiKey)
		c.Next()
	})
	router.Use(RequireGroupAssignment(settingService, GatewayProtocolErrorWriter))
	router.POST("/v1/chat/completions", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.POST("/v1/messages", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), `"error"`)
	require.NotContains(t, w.Body.String(), `"type":"error"`)

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), `"type":"error"`)
}

func TestIsGatewayAPIPath(t *testing.T) {
	require.True(t, isGatewayAPIPath("/v1/messages"))
	require.True(t, isGatewayAPIPath("/v1/chat/completions"))
	require.True(t, isGatewayAPIPath("/v1beta/models/x:generateContent"))
	require.True(t, isGatewayAPIPath("/responses"))
	require.False(t, isGatewayAPIPath("/api/v1/users"))
	require.False(t, isGatewayAPIPath("/t"))
}
