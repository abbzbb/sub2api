package service

import (
	"time"

	"github.com/Wei-Shaw/sub2api/tools/warp-gateway/internal/runtime"
)

func (m *Manager) InstanceMapsContain(id string) (hasMu, hasBackoff, hasProbe bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	_, hasMu = m.instanceMu[id]
	_, hasBackoff = m.startBackoff[id]
	m.mu.Unlock()
	m.probeMu.Lock()
	_, hasProbe = m.lastProbe[id]
	m.probeMu.Unlock()
	return
}

func (m *Manager) SeedInstanceMapsForTest(id string) {
	if m == nil {
		return
	}
	_ = m.lockInstance(id)
	m.noteReconcileStartFailure(id)
	m.probeMu.Lock()
	if m.lastProbe == nil {
		m.lastProbe = map[string]time.Time{}
	}
	m.lastProbe[id] = time.Now()
	m.probeMu.Unlock()
}

type DuplicateRotateMember = duplicateRotateMember

func PickDuplicateRotateVictim(dups map[string][]duplicateRotateMember, lastRotate map[string]time.Time, now time.Time, cooldown time.Duration) (string, string) {
	return pickDuplicateRotateVictim(dups, lastRotate, now, cooldown)
}

func DupRotateMember(id string, port int) duplicateRotateMember {
	return duplicateRotateMember{ID: id, Port: port, Running: true}
}

func DupRotateMemberStatus(id string, port int, running bool) duplicateRotateMember {
	return duplicateRotateMember{ID: id, Port: port, Running: running}
}

func (m *Manager) FailRuntimeForTest(id string, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	h := m.handles[id]
	m.mu.Unlock()
	runtime.MockForceExit(h, err)
}
