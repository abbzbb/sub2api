package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSettingsCodexTicketProxyWriteReadAndHotReload(t *testing.T) {
	key := service.SettingKeyOpenAICodexTicketHarvestProxyURL
	oldProxy := "http://user:old-secret@old.example.com:8080"
	newProxy := "socks5h://user:new-secret@new.example.com:1080"
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{key: oldProxy})
	require.Equal(t, oldProxy, h.settingService.GetOpenAICodexTicketHarvestProxyURL(context.Background()))
	rec := doUpdateSettings(t, h, map[string]any{key: newProxy}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, newProxy, repo.values[key])
	require.Equal(t, newProxy, h.settingService.GetOpenAICodexTicketHarvestProxyURL(context.Background()))
	require.NotContains(t, rec.Body.String(), "new-secret")
	require.Contains(t, rec.Body.String(), `"openai_codex_ticket_harvest_proxy_configured":true`)
	// Omission, empty input and the masked GET value all preserve the real secret.
	for _, body := range []map[string]any{{"site_name": "updated"}, {key: ""}, {key: service.MaskProxyURL(newProxy)}} {
		rec = doUpdateSettings(t, h, body, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, newProxy, repo.values[key])
	}
	get := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(get)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
	h.GetSettings(c)
	require.Equal(t, http.StatusOK, get.Code)
	require.NotContains(t, get.Body.String(), "new-secret")
	require.Contains(t, get.Body.String(), "new.example.com")
}

func TestSettingsCodexTicketRejectEnableWithoutHarvestProxy(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	rec := doUpdateSettings(t, h, map[string]any{"openai_codex_ticket_enabled": true}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "harvest proxy is required")
	require.NotEqual(t, "true", repo.values[service.SettingKeyOpenAICodexTicketEnabled])
}

func TestSettingsCodexTicketFailClosedWriteAndRead(t *testing.T) {
	key := service.SettingKeyOpenAICodexTicketFailClosed
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{
		service.SettingKeyOpenAICodexTicketHarvestProxyURL: "socks5h://user:secret@harvest.example.com:1080",
		key: "true",
	})
	rec := doUpdateSettings(t, h, map[string]any{key: false}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "false", repo.values[key])
	require.False(t, h.settingService.GetOpenAICodexTicketFailClosed(context.Background(), true))
	require.Contains(t, rec.Body.String(), `"openai_codex_ticket_fail_closed":false`)
	rec = doUpdateSettings(t, h, map[string]any{"site_name": "updated"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "false", repo.values[key])
}

func TestSettingsCodexTicketRejectInvalidProxyWithoutLeakingPassword(t *testing.T) {
	key := service.SettingKeyOpenAICodexTicketHarvestProxyURL
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{key: "http://previous.example.com:8080"})
	rec := doUpdateSettings(t, h, map[string]any{key: "ftp://user:invalid-secret@proxy.example.com:21"}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "invalid-secret")
	require.Equal(t, "http://previous.example.com:8080", repo.values[key])
}
