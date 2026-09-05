package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func mustWarpClient(t *testing.T, cfg WarpGatewayConfig) *WarpGatewayClient {
	t.Helper()
	c, err := NewWarpGatewayClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBuildAttachPlan_Phase3(t *testing.T) {
	snap := &WarpPoolSnapshot{
		Instances: []WarpInstance{
			{ID: "a", Name: "01", ListenHost: "127.0.0.1", ListenPort: 41001, Status: "running", ExitIP: "1.1.1.1"},
			{ID: "b", Name: "02", ListenHost: "127.0.0.1", ListenPort: 41002, Status: "unhealthy", ExitIP: "1.1.1.1"},
		},
		UnhealthyIDs: []string{"b"},
		DuplicateIPs: map[string][]string{"1.1.1.1": {"a", "b"}},
		HealthyCount: 1,
		TotalCount:   2,
	}
	plan := BuildAttachPlan(snap, "warp-pool")
	if plan.SuggestedGroupName != "warp-pool" {
		t.Fatal(plan.SuggestedGroupName)
	}
	if len(plan.ProxySpecs) != 2 {
		t.Fatalf("specs=%d", len(plan.ProxySpecs))
	}
	if plan.ProxySpecs[0].Protocol != "socks5h" {
		t.Fatal(plan.ProxySpecs[0].Protocol)
	}
	if len(plan.DetachProxyNames) == 0 {
		t.Fatal("expected detach for unhealthy")
	}
	if len(plan.DuplicateExitIPs["1.1.1.1"]) != 2 {
		t.Fatalf("dups=%v", plan.DuplicateExitIPs)
	}
}

func TestBuildAttachPlan_NonRunningStatusesDetach(t *testing.T) {
	snap := &WarpPoolSnapshot{
		Instances: []WarpInstance{
			{ID: "run", Name: "run", ListenHost: "127.0.0.1", ListenPort: 41001, Status: "running"},
			{ID: "stop", Name: "stop", ListenHost: "127.0.0.1", ListenPort: 41002, Status: "stopped"},
			{ID: "start", Name: "start", ListenHost: "127.0.0.1", ListenPort: 41003, Status: "starting"},
			{ID: "reg", Name: "reg", ListenHost: "127.0.0.1", ListenPort: 41004, Status: "registered"},
		},
		TotalCount:   4,
		HealthyCount: 1,
	}
	plan := BuildAttachPlan(snap, "warp-pool")
	if plan.ProxySpecs[0].Status != StatusActive {
		t.Fatalf("running spec status=%s", plan.ProxySpecs[0].Status)
	}
	for i, spec := range plan.ProxySpecs[1:] {
		if spec.Status != StatusError {
			t.Fatalf("spec %d status=%s want error", i+1, spec.Status)
		}
	}
	if len(plan.DetachProxyNames) != 3 {
		t.Fatalf("detach=%v", plan.DetachProxyNames)
	}
}

func TestBuildAttachPlan_DetachProxyNamesDeduped(t *testing.T) {
	snap := &WarpPoolSnapshot{
		Instances: []WarpInstance{
			{ID: "bad", Name: "01", ListenHost: "127.0.0.1", ListenPort: 41002, Status: "unhealthy"},
		},
		UnhealthyIDs: []string{"bad"},
		TotalCount:   1,
	}
	plan := BuildAttachPlan(snap, "warp-pool")
	if len(plan.DetachProxyNames) != 1 {
		t.Fatalf("detach=%v want 1", plan.DetachProxyNames)
	}
}

func TestBuildAttachPlan_DisambiguatesDuplicateInstanceNames(t *testing.T) {
	snap := &WarpPoolSnapshot{
		Instances: []WarpInstance{
			{ID: "a1", Name: "warp-01", ListenHost: "127.0.0.1", ListenPort: 20001, Status: "running"},
			{ID: "a2", Name: "warp-01", ListenHost: "127.0.0.1", ListenPort: 20002, Status: "running"},
		},
		TotalCount:   2,
		HealthyCount: 2,
	}
	plan := BuildAttachPlan(snap, "warp-pool")
	if len(plan.ProxySpecs) != 2 {
		t.Fatalf("specs=%d", len(plan.ProxySpecs))
	}
	if plan.ProxySpecs[0].Name == plan.ProxySpecs[1].Name {
		t.Fatalf("expected unique proxy names, both %q", plan.ProxySpecs[0].Name)
	}
	if plan.ProxySpecs[1].Name != "warp-warp-01-20002" {
		t.Fatalf("second name=%q", plan.ProxySpecs[1].Name)
	}
}

func TestWarpGatewayClient_ListAndSnapshot(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/instances", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"instances": []WarpInstance{{ID: "x", Name: "n", ListenPort: 41001, Status: "running"}},
		})
	})
	mux.HandleFunc("/v1/pools/snapshot", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(WarpPoolSnapshot{TotalCount: 1, HealthyCount: 1, SocksURLs: []string{"socks5h://127.0.0.1:41001"}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := mustWarpClient(t, WarpGatewayConfig{
		Enabled: true,
		BaseURL: srv.URL,
		Timeout: 2 * time.Second,
	})
	list, err := c.ListInstances(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list err=%v len=%d", err, len(list))
	}
	snap, err := c.PoolSnapshot(context.Background())
	if err != nil || snap.TotalCount != 1 {
		t.Fatalf("snap err=%v %#v", err, snap)
	}
	if list[0].SocksURL() != "socks5h://127.0.0.1:41001" {
		t.Fatal(list[0].SocksURL())
	}
}

func TestMutatingTimeoutAtLeast90s(t *testing.T) {
	c := mustWarpClient(t, WarpGatewayConfig{Timeout: 3 * time.Second})
	if got := c.mutatingTimeout(); got < 90*time.Second {
		t.Fatalf("mutatingTimeout=%s want >= 90s", got)
	}
}

func TestNewWarpGatewayClientRejectsBadTLS(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, []byte("not-a-cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewWarpGatewayClient(WarpGatewayConfig{TLSCAFile: ca})
	if err == nil {
		t.Fatal("expected TLS CA parse error")
	}

	disabled := &config.Config{}
	disabled.Warp.Enabled = false
	disabled.Warp.Gateway.TLSCAFile = ca
	client, err := ProvideWarpGatewayClient(disabled)
	if err != nil {
		t.Fatalf("disabled warp must skip leftover TLS path: %v", err)
	}
	if client == nil || client.Enabled() {
		t.Fatal("expected disabled warp client")
	}

	enabled := &config.Config{}
	enabled.Warp.Enabled = true
	enabled.Warp.Gateway.TLSCAFile = ca
	if _, err := ProvideWarpGatewayClient(enabled); err == nil {
		t.Fatal("enabled warp must still reject bad TLS")
	}
}
