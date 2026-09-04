package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/config"
	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/health"
	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/register"
	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/runtime"
	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/store"
	"github.com/google/uuid"
)

// Manager orchestrates instance lifecycle, health, rotate, and pooling helpers.
type Manager struct {
	cfg     config.Config
	store   *store.Store
	runtime runtime.Manager
	log     *slog.Logger

	mu        sync.Mutex
	handles   map[string]runtime.Handle
	cancels   map[string]context.CancelFunc
	starting  map[string]struct{}
	lastProbe map[string]time.Time
	probeMu   sync.Mutex
}

func NewManager(cfg config.Config, st *store.Store, rt runtime.Manager, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		cfg:       cfg,
		store:     st,
		runtime:   rt,
		log:       log,
		handles:   make(map[string]runtime.Handle),
		cancels:   make(map[string]context.CancelFunc),
		starting:  make(map[string]struct{}),
		lastProbe: make(map[string]time.Time),
	}
}

type CreateRequest struct {
	Name          string             `json:"name"`
	ListenHost    string             `json:"listen_host"`
	ListenPort    int                `json:"listen_port"`
	Profile       store.Profile      `json:"profile"`
	DesiredState  store.DesiredState `json:"desired_state"`
	SocksAuthUser string             `json:"socks_auth_user"`
	SocksAuthPass string             `json:"socks_auth_pass"`
	// AutoStart defaults true when desired_state empty.
	AutoStart *bool `json:"auto_start"`
}

func (m *Manager) Create(ctx context.Context, req CreateRequest) (*store.Instance, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	host := req.ListenHost
	if host == "" {
		host = m.cfg.DefaultHost
	}
	port, err := m.store.AllocatePort(req.ListenPort)
	if err != nil {
		return nil, err
	}
	created := false
	defer func() {
		if !created {
			m.store.ReleasePort(port)
		}
	}()
	desired := req.DesiredState
	if desired == "" {
		desired = store.DesiredRunning
	}
	inst := &store.Instance{
		ID:            uuid.NewString(),
		Name:          req.Name,
		ListenHost:    host,
		ListenPort:    port,
		Runtime:       m.runtime.Name(),
		Status:        store.StatusRegistered,
		DesiredState:  desired,
		Profile:       req.Profile,
		SocksAuthUser: req.SocksAuthUser,
		SocksAuthPass: req.SocksAuthPass,
	}
	if err := m.store.Create(inst); err != nil {
		return nil, err
	}
	created = true
	auto := true
	if req.AutoStart != nil {
		auto = *req.AutoStart
	}
	if auto && desired == store.DesiredRunning {
		if err := m.Start(ctx, inst.ID); err != nil {
			_, _ = m.store.Update(inst.ID, func(i *store.Instance) {
				i.Status = store.StatusError
				i.LastError = err.Error()
			})
			got, _ := m.store.Get(inst.ID)
			if got == nil {
				return nil, err
			}
			out := RedactInstance(*got)
			return &out, nil
		}
	}
	got, err := m.store.Get(inst.ID)
	if err != nil {
		return nil, err
	}
	out := RedactInstance(*got)
	return &out, nil
}

// CreatePool creates N instances and returns them (Phase 3).
type CreatePoolRequest struct {
	NamePrefix string          `json:"name_prefix"`
	Count      int             `json:"count"`
	Profiles   []store.Profile `json:"profiles"` // optional per-instance; if empty, mock or register
	StartPort  int             `json:"start_port"`
	AutoStart  *bool           `json:"auto_start"`
	// Register when true, auto-registers free Cloudflare WARP profiles via API
	// for missing profile slots (required for real sing-box pools).
	Register bool `json:"register"`
}

func (m *Manager) CreatePool(ctx context.Context, req CreatePoolRequest) ([]store.Instance, error) {
	if req.Count <= 0 {
		return nil, fmt.Errorf("count must be > 0")
	}
	if req.Count > register.MaxRegisterPerCall {
		return nil, fmt.Errorf("count too large (max %d)", register.MaxRegisterPerCall)
	}
	prefix := req.NamePrefix
	if prefix == "" {
		prefix = "warp"
	}
	// Real WARP runtime without profiles → auto-register free accounts.
	needRegister := req.Register
	if !needRegister && m.runtime.Name() == "sing-box" && len(req.Profiles) < req.Count {
		needRegister = true
	}
	// Index of the first auto-registered profile; profiles at or after this index
	// that never made it into an instance are unregistered on partial failure so
	// Cloudflare free devices do not leak.
	autoRegisteredFrom := -1
	if needRegister && len(req.Profiles) < req.Count {
		missing := req.Count - len(req.Profiles)
		regs, err := register.RegisterMany(ctx, missing)
		if err != nil {
			// RegisterMany returns the partial batch alongside the error; release it.
			m.unregisterProfiles(regs)
			return nil, fmt.Errorf("auto-register warp profiles: %w", err)
		}
		autoRegisteredFrom = len(req.Profiles)
		for _, r := range regs {
			req.Profiles = append(req.Profiles, r.Profile)
		}
	}
	names, err := m.allocatePoolNames(prefix, req.Count)
	if err != nil {
		if autoRegisteredFrom >= 0 {
			m.unregisterUnusedProfiles(req.Profiles, autoRegisteredFrom)
		}
		return nil, err
	}
	out := make([]store.Instance, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		var profile store.Profile
		if i < len(req.Profiles) {
			profile = req.Profiles[i]
		} else if m.runtime.Name() == "sing-box" {
			return out, fmt.Errorf("sing-box pool requires profiles or register=true (missing profile for member %d)", i+1)
		} else {
			// Distinct mock exit IPs for dedup testing.
			profile = store.Profile{MockExitIP: fmt.Sprintf("203.0.113.%d", 10+i)}
		}
		port := 0
		if req.StartPort > 0 {
			port = req.StartPort + i
		}
		inst, err := m.Create(ctx, CreateRequest{
			Name:       names[i],
			ListenPort: port,
			Profile:    profile,
			AutoStart:  req.AutoStart,
		})
		if err != nil {
			// Members i..Count-1 never got an instance: release their auto-registered devices.
			if autoRegisteredFrom >= 0 {
				from := i
				if from < autoRegisteredFrom {
					from = autoRegisteredFrom
				}
				m.unregisterUnusedProfiles(req.Profiles, from)
			}
			return out, fmt.Errorf("create pool member %d (%s): %w", i+1, names[i], err)
		}
		// Stagger real WARP handshakes.
		if m.runtime.Name() == "sing-box" && i+1 < req.Count {
			time.Sleep(1500 * time.Millisecond)
		}
		out = append(out, RedactInstance(*inst))
	}
	return out, nil
}

// unregisterProfiles best-effort deletes Cloudflare free devices for a partial
// registration batch that will never be attached to an instance.
func (m *Manager) unregisterProfiles(regs []register.Result) {
	profiles := make([]store.Profile, 0, len(regs))
	for _, r := range regs {
		profiles = append(profiles, r.Profile)
	}
	m.unregisterUnusedProfiles(profiles, 0)
}

// unregisterUnusedProfiles best-effort deletes devices for profiles[from:] that
// carry registration credentials. Uses a detached context: the caller's ctx may
// already be cancelled, which is exactly when cleanup matters most.
func (m *Manager) unregisterUnusedProfiles(profiles []store.Profile, from int) {
	if from < 0 {
		from = 0
	}
	for i := from; i < len(profiles); i++ {
		p := profiles[i]
		if p.DeviceID == "" || p.AccessToken == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := register.Unregister(ctx, p.DeviceID, p.AccessToken)
		cancel()
		if err != nil {
			m.log.Warn("warp device cleanup after partial pool create failed",
				"device_id", p.DeviceID, "err", err)
			continue
		}
		m.log.Info("warp device released after partial pool create", "device_id", p.DeviceID)
	}
}

// allocatePoolNames returns count unique names under prefix, skipping ones already
// present in the store. First batch → prefix-01..N; second batch continues from
// the next free index so multi-add never collides (e.g. warp-01..03 then warp-04..06).
func (m *Manager) allocatePoolNames(prefix string, count int) ([]string, error) {
	if count <= 0 {
		return nil, fmt.Errorf("count must be > 0")
	}
	existing := map[string]struct{}{}
	if m.store != nil {
		existing = m.store.NameSet()
	}
	names := make([]string, 0, count)
	for i := 1; len(names) < count; i++ {
		var name string
		if i < 100 {
			name = fmt.Sprintf("%s-%02d", prefix, i)
		} else {
			name = fmt.Sprintf("%s-%d", prefix, i)
		}
		if _, taken := existing[name]; taken {
			if i > 100000 {
				return nil, fmt.Errorf("could not allocate %d unique names with prefix %q", count, prefix)
			}
			continue
		}
		existing[name] = struct{}{}
		names = append(names, name)
		if i > 100000 {
			return nil, fmt.Errorf("could not allocate %d unique names with prefix %q", count, prefix)
		}
	}
	return names, nil
}

// RegisterProfiles registers free WARP profiles and returns them for pool creation (internal secrets kept until Create).
func (m *Manager) RegisterProfiles(ctx context.Context, count int) ([]store.Profile, error) {
	regs, err := register.RegisterMany(ctx, count)
	if err != nil {
		return nil, err
	}
	full := make([]store.Profile, 0, len(regs))
	for _, r := range regs {
		full = append(full, r.Profile)
	}
	return full, nil
}

// RedactInstance strips secrets for API responses.
func RedactInstance(inst store.Instance) store.Instance {
	if inst.Profile.PrivateKey != "" {
		inst.Profile.PrivateKey = "***"
	}
	if inst.Profile.AccessToken != "" {
		inst.Profile.AccessToken = "***"
	}
	if inst.Profile.LicenseKey != "" {
		inst.Profile.LicenseKey = "***"
	}
	if inst.SocksAuthPass != "" {
		inst.SocksAuthPass = "***"
	}
	return inst
}

// RedactInstances maps RedactInstance over a slice.
func RedactInstances(list []store.Instance) []store.Instance {
	out := make([]store.Instance, len(list))
	for i := range list {
		out[i] = RedactInstance(list[i])
	}
	return out
}

// ErrStartInFlight is returned when a second Start races an in-flight Start.
var ErrStartInFlight = errors.New("instance start already in progress")

func (m *Manager) Start(ctx context.Context, id string) error {
	inst, err := m.store.Get(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if _, ok := m.handles[id]; ok {
		m.mu.Unlock()
		return nil
	}
	if _, inflight := m.starting[id]; inflight {
		m.mu.Unlock()
		return ErrStartInFlight
	}
	m.starting[id] = struct{}{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.starting, id)
		m.mu.Unlock()
	}()

	_, _ = m.store.Update(id, func(i *store.Instance) {
		i.Status = store.StatusStarting
		i.DesiredState = store.DesiredRunning
		i.LastError = ""
	})

	runCtx, cancel := context.WithCancel(context.Background())
	h, err := m.runtime.Start(runCtx, inst)
	if err != nil {
		cancel()
		_, _ = m.store.Update(id, func(i *store.Instance) {
			i.Status = store.StatusError
			i.LastError = err.Error()
		})
		return err
	}

	m.mu.Lock()
	if existing, ok := m.handles[id]; ok {
		m.mu.Unlock()
		cancel()
		stopCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		_ = h.Stop(stopCtx)
		c()
		_ = existing
		return nil
	}
	m.handles[id] = h
	m.cancels[id] = cancel
	m.mu.Unlock()

	_, _ = m.store.Update(id, func(i *store.Instance) {
		i.Status = store.StatusRunning
		i.LastError = ""
	})
	m.watchHandle(id, h)

	if current, getErr := m.store.Get(id); getErr == nil && current.DesiredState == store.DesiredStopped {
		return m.Stop(ctx, id)
	}

	// Allow one probe after start; HealthAll within the min interval is skipped.
	m.probeMu.Lock()
	delete(m.lastProbe, id)
	m.probeMu.Unlock()
	_ = m.HealthCheck(ctx, id)
	return nil
}

func (m *Manager) watchHandle(id string, h runtime.Handle) {
	if h == nil {
		return
	}
	done := h.Done()
	if done == nil {
		return
	}
	go func() {
		err, ok := <-done
		if !ok {
			err = fmt.Errorf("runtime exited")
		}
		m.mu.Lock()
		cur, exists := m.handles[id]
		if !exists || cur != h {
			m.mu.Unlock()
			return
		}
		delete(m.handles, id)
		if cancel := m.cancels[id]; cancel != nil {
			cancel()
		}
		delete(m.cancels, id)
		m.mu.Unlock()
		msg := "runtime exited unexpectedly"
		if err != nil {
			msg = err.Error()
		}
		_, _ = m.store.Update(id, func(i *store.Instance) {
			if i.DesiredState == store.DesiredRunning {
				i.Status = store.StatusError
				i.LastError = msg
			}
		})
	}()
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	m.mu.Lock()
	h, ok := m.handles[id]
	cancel := m.cancels[id]
	if ok {
		delete(m.handles, id)
		delete(m.cancels, id)
	}
	m.mu.Unlock()

	_, _ = m.store.Update(id, func(i *store.Instance) {
		i.Status = store.StatusStopping
		i.DesiredState = store.DesiredStopped
	})

	if cancel != nil {
		cancel()
	}
	if h != nil {
		stopCtx, c := context.WithTimeout(ctx, 5*time.Second)
		defer c()
		_ = h.Stop(stopCtx)
	}
	_, _ = m.store.Update(id, func(i *store.Instance) {
		i.Status = store.StatusStopped
	})
	return nil
}

func (m *Manager) Restart(ctx context.Context, id string) error {
	_ = m.Stop(ctx, id)
	_, _ = m.store.Update(id, func(i *store.Instance) {
		i.DesiredState = store.DesiredRunning
	})
	// Allow OS to release SOCKS listen port before re-bind. Do not abort Start
	// if the caller context is already cancelled (client timeout after Stop).
	timer := time.NewTimer(80 * time.Millisecond)
	select {
	case <-ctx.Done():
		if !timer.Stop() {
			<-timer.C
		}
	case <-timer.C:
	}
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	return m.Start(startCtx, id)
}

// Rotate restarts instance; optional new profile (Phase 3).
// When newProfile is nil and runtime is sing-box, re-registers a free WARP profile
// and best-effort unregisters the previous Cloudflare device.
func (m *Manager) Rotate(ctx context.Context, id string, newProfile *store.Profile) (*store.Instance, error) {
	if newProfile != nil {
		if newProfile.PrivateKey == "" && len(newProfile.Peers) == 0 && newProfile.MockExitIP == "" {
			return nil, fmt.Errorf("profile is empty")
		}
		if m.runtime.Name() == "sing-box" && (newProfile.PrivateKey == "" || len(newProfile.Peers) == 0) {
			return nil, fmt.Errorf("profile requires private_key and peers")
		}
		old, _ := m.store.Get(id)
		if _, err := m.store.Update(id, func(i *store.Instance) {
			i.Profile = *newProfile
		}); err != nil {
			return nil, err
		}
		if old != nil {
			if uerr := m.unregisterCloudflare(ctx, old.Profile); uerr != nil {
				m.log.Warn("rotate: unregister old cloudflare device failed", "id", id, "err", uerr)
			}
		}
	} else if m.runtime.Name() == "sing-box" {
		old, _ := m.store.Get(id)
		reg, err := register.RegisterFree(ctx)
		if err != nil {
			return nil, fmt.Errorf("rotate re-register: %w", err)
		}
		if _, err := m.store.Update(id, func(i *store.Instance) {
			i.Profile = reg.Profile
		}); err != nil {
			return nil, err
		}
		// Best-effort CF cleanup of previous device (do not fail rotate).
		if old != nil {
			if uerr := m.unregisterCloudflare(ctx, old.Profile); uerr != nil {
				m.log.Warn("rotate: unregister old cloudflare device failed", "id", id, "err", uerr)
			}
		}
	}
	if err := m.Restart(ctx, id); err != nil {
		return nil, err
	}
	inst, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}
	out := RedactInstance(*inst)
	return &out, nil
}

// DeleteOptions controls Cloudflare deregistration behavior.
type DeleteOptions struct {
	// DeregisterCloudflare defaults to true: call CF DELETE /reg/{device_id}.
	// Set false to only stop local instance/store.
	DeregisterCloudflare *bool `json:"deregister_cloudflare"`
}

func (m *Manager) Delete(ctx context.Context, id string) error {
	return m.DeleteWithOptions(ctx, id, DeleteOptions{})
}

// DeleteWithOptions stops instance, optionally unregisters CF device, removes store entry.
func (m *Manager) DeleteWithOptions(ctx context.Context, id string, opts DeleteOptions) error {
	inst, err := m.store.Get(id)
	if err != nil {
		return err
	}
	deregister := true
	if opts.DeregisterCloudflare != nil {
		deregister = *opts.DeregisterCloudflare
	}
	if deregister {
		if uerr := m.unregisterCloudflare(ctx, inst.Profile); uerr != nil {
			// Log but still delete local state so ops can recover.
			m.log.Warn("cloudflare unregister failed; continuing local delete", "id", id, "device_id", inst.Profile.DeviceID, "err", uerr)
		}
	}
	_ = m.Stop(ctx, id)
	if err := m.store.Delete(id); err != nil {
		return err
	}
	instDir := filepath.Join(m.cfg.DataDir, "instances", id)
	if err := os.RemoveAll(instDir); err != nil {
		m.log.Warn("remove instance dir failed", "id", id, "dir", instDir, "err", err)
	}
	return nil
}

func (m *Manager) unregisterCloudflare(ctx context.Context, p store.Profile) error {
	if p.DeviceID == "" || p.AccessToken == "" {
		return nil // nothing to unregister (mock or legacy instance)
	}
	return register.Unregister(ctx, p.DeviceID, p.AccessToken)
}

func (m *Manager) HasRuntimeHandle(id string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.handles[id]
	return ok
}

func (m *Manager) Get(id string) (*store.Instance, error) {
	inst, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}
	out := RedactInstance(*inst)
	return &out, nil
}
func (m *Manager) List() []store.Instance { return RedactInstances(m.store.List()) }

// GetRaw returns instance with secrets (runtime internal use only).
func (m *Manager) GetRaw(id string) (*store.Instance, error) { return m.store.Get(id) }

const healthProbeMinInterval = 15 * time.Second

func (m *Manager) HealthCheck(ctx context.Context, id string) error {
	m.probeMu.Lock()
	if last, ok := m.lastProbe[id]; ok && time.Since(last) < healthProbeMinInterval {
		m.probeMu.Unlock()
		return nil
	}
	m.lastProbe[id] = time.Now()
	m.probeMu.Unlock()

	inst, err := m.store.Get(id)
	if err != nil {
		return err
	}
	var res health.Result
	probeURL := m.cfg.ProbeURL
	if m.runtime.Name() == "mock" || (probeURL != "" && isMockProbe(probeURL)) {
		res = health.ProbeMock(inst.Profile.MockExitIP)
	} else {
		timeout := 8 * time.Second
		res = health.ProbeViaSOCKS(ctx, inst.ListenHost, inst.ListenPort, inst.SocksAuthUser, inst.SocksAuthPass, probeURL, timeout)
	}
	// A cancelled/expired caller context is not evidence about the instance:
	// do not count it as a failure (would flip healthy members to unhealthy and
	// trigger auto-detach when the backend's request deadline is simply too short).
	if !res.OK && ctx.Err() != nil {
		return ctx.Err()
	}
	now := time.Now().UTC()
	_, err = m.store.Update(id, func(i *store.Instance) {
		i.LastHealthAt = &now
		lat := res.LatencyMs
		i.LatencyMs = &lat
		if res.OK {
			i.FailCount = 0
			i.ExitIP = res.ExitIP
			i.ExitColo = res.Colo
			i.LastError = ""
			if i.DesiredState == store.DesiredRunning {
				i.Status = store.StatusRunning
			}
		} else {
			i.FailCount++
			i.LastError = res.Error
			if i.FailCount >= m.cfg.UnhealthyAfter {
				i.Status = store.StatusUnhealthy
			}
		}
	})
	return err
}

func isMockProbe(u string) bool {
	parsed, err := url.Parse(u)
	return err == nil && parsed.Scheme == "mock"
}

// healthAllConcurrency bounds parallel probes in HealthAll. Sequential probing
// of a 50-member pool at 8s/probe could take ~7 minutes and blow the backend's
// request deadline; probes are independent SOCKS connections, so a small
// worker pool is safe.
const healthAllConcurrency = 8

// HealthAll probes every instance; returns unhealthy IDs for auto-detach consumers.
// Returns ctx.Err() when the caller deadline expired before all probes completed
// so consumers do not treat a partial result as a complete health picture.
func (m *Manager) HealthAll(ctx context.Context) (unhealthy []string, err error) {
	ids := make([]string, 0)
	for _, inst := range m.store.List() {
		if inst.DesiredState != store.DesiredRunning {
			continue
		}
		ids = append(ids, inst.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	sem := make(chan struct{}, healthAllConcurrency)
	var wg sync.WaitGroup
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			if e := m.HealthCheck(ctx, id); e != nil && ctx.Err() == nil {
				m.log.Warn("health check error", "id", id, "err", e)
			}
		}(id)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for _, id := range ids {
		cur, _ := m.store.Get(id)
		if cur != nil && cur.Status == store.StatusUnhealthy {
			unhealthy = append(unhealthy, cur.ID)
		}
	}
	return unhealthy, nil
}

// ExitIPDuplicates reports exit IPs shared by multiple running instances (Phase 3).
func (m *Manager) ExitIPDuplicates() map[string][]string {
	byIP := map[string][]string{}
	for _, inst := range m.store.List() {
		if inst.ExitIP == "" {
			continue
		}
		if inst.Status != store.StatusRunning && inst.Status != store.StatusUnhealthy {
			continue
		}
		byIP[inst.ExitIP] = append(byIP[inst.ExitIP], inst.ID)
	}
	dups := map[string][]string{}
	for ip, ids := range byIP {
		if len(ids) > 1 {
			dups[ip] = ids
		}
	}
	return dups
}

// Reconcile brings runtime in line with desired_state.
func (m *Manager) Reconcile(ctx context.Context) {
	for _, inst := range m.store.List() {
		m.mu.Lock()
		_, running := m.handles[inst.ID]
		m.mu.Unlock()
		switch inst.DesiredState {
		case store.DesiredRunning:
			if !running {
				if err := m.Start(ctx, inst.ID); err != nil {
					m.log.Error("reconcile start failed", "id", inst.ID, "err", err)
				}
			}
		case store.DesiredStopped:
			if running {
				if err := m.Stop(ctx, inst.ID); err != nil {
					m.log.Error("reconcile stop failed", "id", inst.ID, "err", err)
				}
			}
		}
	}
}

// RunBackground starts health loop and periodic reconcile for crashed runtimes.
func (m *Manager) RunBackground(ctx context.Context) {
	if m.cfg.ReconcileOnStart {
		m.Reconcile(ctx)
	}
	interval := m.cfg.HealthInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	rt := time.NewTicker(interval)
	defer rt.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			unhealthy, _ := m.HealthAll(ctx)
			if len(unhealthy) > 0 {
				m.log.Warn("unhealthy instances", "ids", unhealthy)
			}
			if dups := m.ExitIPDuplicates(); len(dups) > 0 {
				m.log.Warn("duplicate exit IPs detected", "dups", dups)
			}
		case <-rt.C:
			m.Reconcile(ctx)
		}
	}
}

// Shutdown stops all handles.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.handles))
	for id := range m.handles {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Stop(ctx, id)
	}
}

// PoolSnapshot is a Phase-3 view for attaching to sub2api ProxyGroup.
type PoolSnapshot struct {
	Instances    []store.Instance    `json:"instances"`
	SocksURLs    []string            `json:"socks_urls"`
	UnhealthyIDs []string            `json:"unhealthy_ids"`
	DuplicateIPs map[string][]string `json:"duplicate_exit_ips"`
	HealthyCount int                 `json:"healthy_count"`
	TotalCount   int                 `json:"total_count"`
}

func (m *Manager) PoolSnapshot() PoolSnapshot {
	list := RedactInstances(m.store.List())
	snap := PoolSnapshot{
		Instances:    list,
		DuplicateIPs: m.ExitIPDuplicates(),
		TotalCount:   len(list),
	}
	for _, inst := range list {
		snap.SocksURLs = append(snap.SocksURLs, inst.SocksURL())
		if inst.Status == store.StatusUnhealthy || inst.Status == store.StatusError {
			snap.UnhealthyIDs = append(snap.UnhealthyIDs, inst.ID)
		}
		if inst.Status == store.StatusRunning {
			snap.HealthyCount++
		}
	}
	return snap
}
