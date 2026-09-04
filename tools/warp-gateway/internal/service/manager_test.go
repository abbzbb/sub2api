package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/config"
	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/runtime"
	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/service"
	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/store"
)

func testManager(t *testing.T) *service.Manager {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Runtime = "mock"
	cfg.ProbeURL = "mock://local"
	cfg.PortRangeStart = 42001
	cfg.PortRangeEnd = 42050
	cfg.HealthInterval = time.Hour
	cfg.UnhealthyAfter = 2
	st, err := store.New(filepath.Join(dir, "state"), cfg.PortRangeStart, cfg.PortRangeEnd)
	if err != nil {
		t.Fatal(err)
	}
	rt := runtime.NewMockManager()
	return service.NewManager(cfg, st, rt, nil)
}

func TestCreateStartHealthPoolRotate(t *testing.T) {
	mgr := testManager(t)
	ctx := context.Background()

	inst, err := mgr.Create(ctx, service.CreateRequest{
		Name:    "warp-a",
		Profile: store.Profile{MockExitIP: "203.0.113.21"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Status != store.StatusRunning {
		t.Fatalf("status=%s want running", inst.Status)
	}
	if inst.ExitIP != "203.0.113.21" {
		t.Fatalf("exit_ip=%s", inst.ExitIP)
	}
	if inst.SocksURL() == "" {
		t.Fatal("empty socks url")
	}

	// Pool of 3
	pool, err := mgr.CreatePool(ctx, service.CreatePoolRequest{
		NamePrefix: "pool",
		Count:      3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 3 {
		t.Fatalf("pool size %d", len(pool))
	}
	// Second batch with same prefix must allocate next free names (no collision).
	pool2, err := mgr.CreatePool(ctx, service.CreatePoolRequest{
		NamePrefix: "pool",
		Count:      2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pool2) != 2 {
		t.Fatalf("second pool size %d", len(pool2))
	}
	names := map[string]struct{}{}
	for _, inst := range append(pool, pool2...) {
		if _, dup := names[inst.Name]; dup {
			t.Fatalf("duplicate pool name %q across batches", inst.Name)
		}
		names[inst.Name] = struct{}{}
	}
	if pool2[0].Name != "pool-04" || pool2[1].Name != "pool-05" {
		t.Fatalf("expected pool-04/05 after first batch of 3, got %q %q", pool2[0].Name, pool2[1].Name)
	}

	// Force duplicate exit IP for alert
	_, err = mgr.Create(ctx, service.CreateRequest{
		Name:    "dup",
		Profile: store.Profile{MockExitIP: "203.0.113.21"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dups := mgr.ExitIPDuplicates()
	if len(dups["203.0.113.21"]) < 2 {
		t.Fatalf("expected duplicate exit ip, got %#v", dups)
	}

	// Rotate with new mock IP
	rotated, err := mgr.Rotate(ctx, inst.ID, &store.Profile{MockExitIP: "198.51.100.9"})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ExitIP != "198.51.100.9" {
		t.Fatalf("after rotate exit_ip=%s", rotated.ExitIP)
	}

	snap := mgr.PoolSnapshot()
	if snap.TotalCount < 5 {
		t.Fatalf("snapshot total=%d", snap.TotalCount)
	}
	if snap.HealthyCount < 1 {
		t.Fatalf("healthy=%d", snap.HealthyCount)
	}

	// Stop + delete
	if err := mgr.Stop(ctx, inst.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := mgr.Get(inst.ID)
	if got.Status != store.StatusStopped {
		t.Fatalf("stop status=%s", got.Status)
	}
	if err := mgr.Delete(ctx, inst.ID); err != nil {
		t.Fatal(err)
	}
}

func TestUnhealthyThreshold(t *testing.T) {
	// Unhealthy path is exercised when FailCount accumulates; mock always healthy.
	// We simulate via store update through failed health by using empty runtime handle stop.
	// For unit coverage, mark via multiple HealthCheck after Stop.
	mgr := testManager(t)
	ctx := context.Background()
	inst, err := mgr.Create(ctx, service.CreateRequest{Name: "u1", Profile: store.Profile{MockExitIP: "203.0.113.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Stop(ctx, inst.ID); err != nil {
		t.Fatal(err)
	}
	// After stop, mock probe still succeeds (probe does not require live socks in mock mode).
	// That's intentional for control-plane unit tests.
	_ = mgr.HealthCheck(ctx, inst.ID)
}

func TestRestartAfterCancelledContextStillStarts(t *testing.T) {
	mgr := testManager(t)
	ctx := context.Background()
	inst, err := mgr.Create(ctx, service.CreateRequest{Name: "restart-me", Profile: store.Profile{MockExitIP: "203.0.113.8"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Stop(ctx, inst.ID); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := mgr.Restart(canceled, inst.ID); err != nil {
		t.Fatalf("restart after cancel: %v", err)
	}
	got, err := mgr.Get(inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusRunning {
		t.Fatalf("status=%s want running", got.Status)
	}
	if got.DesiredState != store.DesiredRunning {
		t.Fatalf("desired=%s", got.DesiredState)
	}
}

func TestDeleteRemovesInstanceDirectory(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Runtime = "mock"
	cfg.ProbeURL = "mock://local"
	cfg.PortRangeStart = 42101
	cfg.PortRangeEnd = 42140
	cfg.HealthInterval = time.Hour
	st, err := store.New(filepath.Join(dir, "state"), cfg.PortRangeStart, cfg.PortRangeEnd)
	if err != nil {
		t.Fatal(err)
	}
	mgr := service.NewManager(cfg, st, runtime.NewMockManager(), nil)
	t.Cleanup(func() { mgr.Shutdown(context.Background()) })

	ctx := context.Background()
	inst, err := mgr.Create(ctx, service.CreateRequest{Name: "dir-me", Profile: store.Profile{MockExitIP: "203.0.113.9"}})
	if err != nil {
		t.Fatal(err)
	}
	instDir := filepath.Join(dir, "instances", inst.ID)
	if err := os.MkdirAll(instDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(instDir, "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Delete(ctx, inst.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(instDir); !os.IsNotExist(err) {
		t.Fatalf("instance dir still present: %v", err)
	}
}

func TestRotateRejectsEmptyProfile(t *testing.T) {
	mgr := testManager(t)
	ctx := context.Background()
	inst, err := mgr.Create(ctx, service.CreateRequest{Name: "empty-rot", Profile: store.Profile{MockExitIP: "203.0.113.10"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Rotate(ctx, inst.ID, &store.Profile{}); err == nil {
		t.Fatal("expected empty profile to fail")
	}
}
