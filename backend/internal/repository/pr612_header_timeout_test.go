package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type pr612SettingRepo struct {
	service.SettingRepository
	mu    sync.Mutex
	value string
}

func TestPR612NilSettingsProviderUsesStaticTimeout(t *testing.T) {
	for _, seconds := range []int{0, 45} {
		cfg := &config.Config{}
		cfg.Gateway.OpenAIResponseHeaderTimeout = seconds
		cfg.Gateway.ResponseHeaderTimeout = 600
		s, ok := ProvideHTTPUpstream(cfg, nil).(*httpUpstreamService)
		require.True(t, ok)
		require.Equal(t, time.Duration(seconds)*time.Second, s.applyProfilePoolSettings(poolSettings{}, service.HTTPUpstreamProfileOpenAI).responseHeaderTimeout)
	}
}

func (r *pr612SettingRepo) GetValue(context.Context, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.value == "" {
		return "", service.ErrSettingNotFound
	}
	return r.value, nil
}

func (r *pr612SettingRepo) Set(_ context.Context, _, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.value = value
	return nil
}

func TestPR612RuntimeDisableRebuildsPoolAndPreservesActiveStream(t *testing.T) {
	finishStream := make(chan struct{})
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(finishStream) }) }
	defer finish()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: begin\n\n")
			_ = http.NewResponseController(w).Flush()
			select {
			case <-finishStream:
				_, _ = io.WriteString(w, "data: done\n\n")
			case <-r.Context().Done():
			}
			return
		}
		// Exceeds the old one-second timer: an actual next request must use 0.
		select {
		case <-time.After(1200 * time.Millisecond):
			_, _ = io.WriteString(w, "slow completion")
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowPrivateHosts = true
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIResponseHeaderTimeout = 1
	cfg.Gateway.ResponseHeaderTimeout = 600
	settings := service.NewSettingService(&pr612SettingRepo{}, cfg)
	s, ok := newHTTPUpstream(cfg, settings).(*httpUpstreamService)
	require.True(t, ok)
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	ctx = service.WithHTTPUpstreamProfile(ctx, service.HTTPUpstreamProfileOpenAI)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL+"/stream", strings.NewReader("synthetic"))
	require.NoError(t, err)
	stream, err := s.Do(req, "", 1, 1)
	require.NoError(t, err)
	defer func() { require.NoError(t, stream.Body.Close()) }()
	prefix := make([]byte, len("data: begin\n\n"))
	_, err = io.ReadFull(stream.Body, prefix)
	require.NoError(t, err)
	oldPool := s.applyProfilePoolSettings(poolSettings{}, service.HTTPUpstreamProfileOpenAI)
	require.NoError(t, settings.SetOpenAIResponseHeaderTimeoutSettings(ctx, &service.OpenAIResponseHeaderTimeoutSettings{Enabled: false, Seconds: 1}))
	newPool := s.applyProfilePoolSettings(poolSettings{}, service.HTTPUpstreamProfileOpenAI)
	require.Zero(t, newPool.responseHeaderTimeout, "disabled never inherits generic 600s or static 1s")
	require.NotEqual(t, buildPoolKey(oldPool, ""), buildPoolKey(newPool, ""))
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL+"/slow", strings.NewReader("synthetic"))
	require.NoError(t, err)
	resp, err := s.Do(req, "", 1, 1)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, "slow completion", string(body))
	// The old stream has remained open longer than its original header timer.
	finish()
	remainder, err := io.ReadAll(stream.Body)
	require.NoError(t, err)
	require.Equal(t, "data: done\n\n", string(remainder), "saving must leave the old active stream intact")
	require.NoError(t, stream.Body.Close())
	requireNoUpstreamInFlight(t, s)
	// Explicit static 0 also bypasses the generic fallback without a DB provider.
	cfg.Gateway.OpenAIResponseHeaderTimeout = 0
	static, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
	require.True(t, ok)
	require.Zero(t, static.applyProfilePoolSettings(poolSettings{}, service.HTTPUpstreamProfileOpenAI).responseHeaderTimeout)
}
