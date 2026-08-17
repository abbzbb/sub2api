//go:build unit

package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayProtocolErrorWriterSelectsEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		path        string
		wantType    string
		wantGoogle  bool
		wantAnthropic bool
	}{
		{name: "completions", path: "/v1/chat/completions", wantType: "permission_error"},
		{name: "responses", path: "/v1/responses", wantType: "permission_error"},
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
			default:
				errObj, _ := body["error"].(map[string]any)
				require.Equal(t, tc.wantType, errObj["type"])
				require.Equal(t, "API Key is not assigned to any group", errObj["message"])
				require.NotContains(t, body, "type")
			}
		})
	}
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
