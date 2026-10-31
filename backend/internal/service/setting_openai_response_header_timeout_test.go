package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newOpenAIHeaderTimeoutTestService(t *testing.T, seconds int) *SettingService {
	t.Helper()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIResponseHeaderTimeout = seconds
	return NewSettingService(&panelRateLimitSettingRepo{values: map[string]string{}}, cfg)
}

func TestDefaultOpenAIResponseHeaderTimeoutSettings(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIResponseHeaderTimeout = 20
	settings := DefaultOpenAIResponseHeaderTimeoutSettings(cfg)
	require.True(t, settings.Enabled)
	require.Equal(t, 20, settings.Seconds)

	cfg.Gateway.OpenAIResponseHeaderTimeout = 0
	settings = DefaultOpenAIResponseHeaderTimeoutSettings(cfg)
	require.False(t, settings.Enabled)
	require.Equal(t, openAIResponseHeaderTimeoutDefaultSeconds, settings.Seconds)

	settings = DefaultOpenAIResponseHeaderTimeoutSettings(nil)
	require.True(t, settings.Enabled)
	require.Equal(t, openAIResponseHeaderTimeoutDefaultSeconds, settings.Seconds)
}

func TestOpenAIResponseHeaderTimeoutSettingsGetMissingUsesConfig(t *testing.T) {
	svc := newOpenAIHeaderTimeoutTestService(t, 45)

	settings, err := svc.GetOpenAIResponseHeaderTimeoutSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 45, settings.Seconds)

	seconds, enabled := svc.GetOpenAIResponseHeaderTimeoutCached(context.Background())
	require.True(t, enabled)
	require.Equal(t, 45, seconds)
}

func TestOpenAIResponseHeaderTimeoutSettingsSetAndCache(t *testing.T) {
	svc := newOpenAIHeaderTimeoutTestService(t, 20)

	require.NoError(t, svc.SetOpenAIResponseHeaderTimeoutSettings(context.Background(), &OpenAIResponseHeaderTimeoutSettings{
		Enabled: true,
		Seconds: 30,
	}))
	seconds, enabled := svc.GetOpenAIResponseHeaderTimeoutCached(context.Background())
	require.True(t, enabled)
	require.Equal(t, 30, seconds)

	stored, err := svc.GetOpenAIResponseHeaderTimeoutSettings(context.Background())
	require.NoError(t, err)
	require.True(t, stored.Enabled)
	require.Equal(t, 30, stored.Seconds)

	require.NoError(t, svc.SetOpenAIResponseHeaderTimeoutSettings(context.Background(), &OpenAIResponseHeaderTimeoutSettings{
		Enabled: false,
		Seconds: 30,
	}))
	seconds, enabled = svc.GetOpenAIResponseHeaderTimeoutCached(context.Background())
	require.False(t, enabled)
	require.Equal(t, 30, seconds)
}

func TestOpenAIResponseHeaderTimeoutSettingsValidation(t *testing.T) {
	svc := newOpenAIHeaderTimeoutTestService(t, 20)
	for _, seconds := range []int{0, -1, openAIResponseHeaderTimeoutMaxSeconds + 1} {
		require.Error(t, svc.SetOpenAIResponseHeaderTimeoutSettings(context.Background(), &OpenAIResponseHeaderTimeoutSettings{
			Enabled: true,
			Seconds: seconds,
		}))
	}
}
