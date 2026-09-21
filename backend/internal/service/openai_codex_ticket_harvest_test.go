package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestPickOpenAICodexHarvestProxies_PrefersUniqueExitIP(t *testing.T) {
	pool := pickOpenAICodexHarvestProxies([]openAICodexHarvestProxy{
		{URL: "socks5h://127.0.0.1:41001", ExitIP: "104.28.222.43", Name: "warp-01"},
		{URL: "socks5h://127.0.0.1:41002", ExitIP: "104.28.222.43", Name: "warp-02"},
		{URL: "socks5h://127.0.0.1:41003", ExitIP: "1.2.3.4", Name: "warp-03"},
	})
	require.Len(t, pool, 1)
	require.Equal(t, "socks5h://127.0.0.1:41003", pool[0].URL)
}

func TestPickOpenAICodexHarvestProxies_AllDuplicateKeepsPool(t *testing.T) {
	in := []openAICodexHarvestProxy{
		{URL: "socks5h://127.0.0.1:41001", ExitIP: "104.28.222.43"},
		{URL: "socks5h://127.0.0.1:41002", ExitIP: "104.28.222.43"},
	}
	got := pickOpenAICodexHarvestProxies(in)
	require.Equal(t, in, got)
}

func TestHarvestProxiesFromSnapshot_AllUnhealthyDoesNotUseSocksURLs(t *testing.T) {
	snap := &WarpPoolSnapshot{
		Instances: []WarpInstance{
			{Name: "warp-01", ListenHost: "127.0.0.1", ListenPort: 41001, Status: "unhealthy", ExitIP: "1.1.1.1"},
		},
		SocksURLs: []string{"socks5h://127.0.0.1:41001"},
	}
	require.Empty(t, harvestProxiesFromSnapshot(snap, ""))
}

func TestHarvestProxiesFromSnapshot_LegacySocksURLsWhenNoInstances(t *testing.T) {
	snap := &WarpPoolSnapshot{SocksURLs: []string{"socks5h://127.0.0.1:41001"}}
	got := harvestProxiesFromSnapshot(snap, "")
	require.Len(t, got, 1)
	require.Equal(t, "socks5h://127.0.0.1:41001", got[0].URL)
}

func TestHarvestProxiesFromSnapshot_SkipsUnhealthyAndDedupsConfigured(t *testing.T) {
	snap := &WarpPoolSnapshot{
		Instances: []WarpInstance{
			{Name: "warp-01", ListenHost: "127.0.0.1", ListenPort: 41001, Status: "running", ExitIP: "1.1.1.1"},
			{Name: "warp-02", ListenHost: "127.0.0.1", ListenPort: 41002, Status: "unhealthy", ExitIP: "2.2.2.2"},
		},
	}
	got := harvestProxiesFromSnapshot(snap, "socks5h://127.0.0.1:41001")
	require.Len(t, got, 1)
	require.Equal(t, "warp-01", got[0].Name)
	require.Equal(t, "1.1.1.1", got[0].ExitIP)
}

func TestPickOpenAICodexTicketHarvestProxy_RoundRobinWarpPool(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pools/snapshot", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(WarpPoolSnapshot{
			Instances: []WarpInstance{
				{Name: "warp-01", ListenHost: "127.0.0.1", ListenPort: 41001, Status: "running", ExitIP: "10.0.0.1"},
				{Name: "warp-02", ListenHost: "127.0.0.1", ListenPort: 41002, Status: "running", ExitIP: "10.0.0.2"},
			},
			HealthyCount: 2,
			TotalCount:   2,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := NewWarpGatewayClient(WarpGatewayConfig{Enabled: true, BaseURL: srv.URL, Timeout: time.Second})
	require.NoError(t, err)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	svc.SetWarpGatewayClient(client)

	first := svc.pickOpenAICodexTicketHarvestProxy(context.Background())
	second := svc.pickOpenAICodexTicketHarvestProxy(context.Background())
	require.NotEqual(t, first.URL, second.URL)
	require.Contains(t, []string{first.URL, second.URL}, "socks5h://127.0.0.1:41001")
	require.Contains(t, []string{first.URL, second.URL}, "socks5h://127.0.0.1:41002")
	require.True(t, svc.openAICodexTicketHarvestAvailable(context.Background()))
	require.True(t, svc.openAICodexTicketFailClosed())
}

func TestOpenAICodexTicketHarvestAvailable_WarpPoolWithoutConfiguredURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pools/snapshot", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(WarpPoolSnapshot{
			Instances: []WarpInstance{
				{Name: "warp-01", ListenHost: "127.0.0.1", ListenPort: 41001, Status: "running", ExitIP: "10.0.0.1"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client, err := NewWarpGatewayClient(WarpGatewayConfig{Enabled: true, BaseURL: srv.URL, Timeout: time.Second})
	require.NoError(t, err)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	svc.SetWarpGatewayClient(client)
	require.True(t, svc.openAICodexTicketHarvestAvailable(context.Background()))
	require.Equal(t, "socks5h://127.0.0.1:41001", svc.pickOpenAICodexTicketHarvestProxy(context.Background()).URL)
}

func TestFailClosedDoesNotProbeWarpSnapshot(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pools/snapshot", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "down", http.StatusBadGateway)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client, err := NewWarpGatewayClient(WarpGatewayConfig{Enabled: true, BaseURL: srv.URL, Timeout: time.Second})
	require.NoError(t, err)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	svc.SetWarpGatewayClient(client)
	require.True(t, svc.openAICodexTicketHarvestConfigured())
	require.True(t, svc.openAICodexTicketFailClosed())
	require.Equal(t, int32(0), hits.Load())
}

func TestListOpenAICodexTicketHarvestProxies_CachesEmptySnapshotError(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/pools/snapshot", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "down", http.StatusBadGateway)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client, err := NewWarpGatewayClient(WarpGatewayConfig{Enabled: true, BaseURL: srv.URL, Timeout: time.Second})
	require.NoError(t, err)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true}, nil)
	svc.SetWarpGatewayClient(client)
	require.Empty(t, svc.listOpenAICodexTicketHarvestProxies(context.Background()))
	require.Empty(t, svc.listOpenAICodexTicketHarvestProxies(context.Background()))
	require.Equal(t, int32(1), hits.Load())
}
