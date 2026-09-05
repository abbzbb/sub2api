//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSeverityAtLeast(t *testing.T) {
	require.True(t, severityAtLeast(connectionRiskSeverityCritical, connectionRiskSeverityHigh))
	require.False(t, severityAtLeast(connectionRiskSeverityLow, connectionRiskSeverityHigh))
	require.True(t, severityAtLeast(connectionRiskSeverityHigh, connectionRiskSeverityHigh))
}

func TestMaskAPIKeyPrefix(t *testing.T) {
	require.Equal(t, "", maskAPIKeyPrefix(""))
	require.Equal(t, "short", maskAPIKeyPrefix("short"))
	require.Equal(t, "sk-test1…", maskAPIKeyPrefix("sk-test1234567890"))
}

func TestHashUserAgent(t *testing.T) {
	require.Equal(t, "empty", HashUserAgent(""))
	require.Equal(t, "empty", HashUserAgent("  "))
	a := HashUserAgent("Claude Code")
	b := HashUserAgent("claude code")
	require.Equal(t, a, b)
	require.Len(t, a, 16)
}

func TestConnectionRiskService_ClearEventThrottle(t *testing.T) {
	fake := &clearThrottleStub{}
	svc := &ConnectionRiskService{signals: fake}
	kid := int64(42)
	svc.clearEventThrottle(context.Background(), &ConnectionRiskEvent{APIKeyID: &kid})
	require.Equal(t, []int64{42}, fake.cleared)
}

// clearThrottleStub implements ConnectionSignalCache with only ClearThrottle observed.
type clearThrottleStub struct {
	cleared   []int64
	throttled []int64
	exempts   []string
}

func (f *clearThrottleStub) EmitAlwaysOn(context.Context, ConnectionSignal, int, int, uint64) (int, error) {
	return 0, nil
}
func (f *clearThrottleStub) EmitEvidence(context.Context, ConnectionSignal) error { return nil }
func (f *clearThrottleStub) IncrSessionMismatch(context.Context, int64) error     { return nil }
func (f *clearThrottleStub) PruneActive(context.Context, int, time.Duration) error {
	return nil
}
func (f *clearThrottleStub) ActiveCards(context.Context) (int64, int64, error) { return 0, 0, nil }
func (f *clearThrottleStub) ReadKeyWindowMetrics(context.Context, int64, int64, int64) (*ConnectionRiskSubjectMetrics, error) {
	return nil, nil
}
func (f *clearThrottleStub) TryDedupe(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (f *clearThrottleStub) IsExempt(context.Context, string, int64) (bool, error) { return false, nil }
func (f *clearThrottleStub) SetExempt(_ context.Context, scope string, id int64, reason string, _ time.Duration) error {
	f.exempts = append(f.exempts, fmt.Sprintf("%s:%d:%s", scope, id, reason))
	return nil
}
func (f *clearThrottleStub) ClearExempt(context.Context, string, int64) error { return nil }
func (f *clearThrottleStub) ListActiveKeys(context.Context, int) ([]int64, error) {
	return nil, nil
}
func (f *clearThrottleStub) ListActiveUsers(context.Context, int) ([]int64, error) {
	return nil, nil
}
func (f *clearThrottleStub) GetKeyOwner(context.Context, int64) (int64, error)    { return 0, nil }
func (f *clearThrottleStub) GetKeyPrefix(context.Context, int64) (string, error)  { return "", nil }
func (f *clearThrottleStub) TrimUAWindow(context.Context, int64, int64) error     { return nil }
func (f *clearThrottleStub) SetThrottle(_ context.Context, keyID int64, _ int, _ int64) error {
	f.throttled = append(f.throttled, keyID)
	return nil
}
func (f *clearThrottleStub) ClearThrottle(_ context.Context, keyID int64) error {
	f.cleared = append(f.cleared, keyID)
	return nil
}
func (f *clearThrottleStub) GetThrottle(context.Context, int64) (int, int64, bool, error) {
	return 0, 0, false, nil
}
func (f *clearThrottleStub) IncrThrottleCount(context.Context, int64) (int, error) { return 0, nil }
func (f *clearThrottleStub) SnapshotBaselineDay(context.Context, int64, string, int64) (bool, error) {
	return false, nil
}
func (f *clearThrottleStub) LoadBaselineSamples(context.Context, int64, []string) ([]int64, error) {
	return nil, nil
}
func (f *clearThrottleStub) SetBaselineP95(context.Context, int64, float64, int) error { return nil }
func (f *clearThrottleStub) GetBaselineP95(context.Context, int64) (float64, int, bool, error) {
	return 0, 0, false, nil
}

type riskAuthInvStub struct {
	invalidated []int64
}

func (s *riskAuthInvStub) InvalidateAuthCacheByUserID(_ context.Context, userID int64) {
	s.invalidated = append(s.invalidated, userID)
}

func TestRiskActionPolicy_AutoDisableUser_UsesUpdateStatusField(t *testing.T) {
	uid := int64(77)
	repo := &mockUserRepo{
		getByIDUser: &User{ID: uid, Role: RoleUser, Status: StatusActive},
	}
	var updated *User
	repo.updateFn = func(_ context.Context, user *User) error {
		cp := *user
		updated = &cp
		return nil
	}
	auth := &riskAuthInvStub{}
	policy := &RiskActionPolicy{users: repo, authInv: auth}

	event := &ConnectionRiskEvent{
		UserID:      &uid,
		SubjectType: ConnectionRiskSubjectUser,
		Severity:    connectionRiskSeverityCritical,
	}
	policy.HandleNewEvent(context.Background(), event, ConnectionRiskSettings{
		Phase:   connectionRiskPhaseAutoDisable,
		Actions: ConnectionRiskActionSettings{AutoDisableEnabled: true},
	})

	require.Equal(t, 1, repo.updateCalls)
	require.Len(t, repo.updateFields, 1)
	require.True(t, repo.updateFields[0].Status, "must use UserUpdateFields{Status:true}, not dead UpdateStatus assert")
	require.NotNil(t, updated)
	require.Equal(t, StatusDisabled, updated.Status)
	require.Equal(t, []int64{uid}, auth.invalidated)
	require.Equal(t, "disabled_user", event.ActionTaken)
}

func TestRiskActionPolicy_AutoDisableUser_SkipsAdmin(t *testing.T) {
	uid := int64(1)
	repo := &mockUserRepo{
		getByIDUser: &User{ID: uid, Role: RoleAdmin, Status: StatusActive},
	}
	policy := &RiskActionPolicy{users: repo}

	event := &ConnectionRiskEvent{
		UserID:      &uid,
		SubjectType: ConnectionRiskSubjectUser,
		Severity:    connectionRiskSeverityCritical,
	}
	policy.HandleNewEvent(context.Background(), event, ConnectionRiskSettings{
		Phase:   connectionRiskPhaseAutoDisable,
		Actions: ConnectionRiskActionSettings{AutoDisableEnabled: true},
	})

	require.Equal(t, 0, repo.updateCalls, "admin must never be auto-disabled")
	require.Empty(t, event.ActionTaken)
}

func TestRiskActionPolicy_AutoDisableRequiresAutoDisablePhase(t *testing.T) {
	uid := int64(88)
	repo := &mockUserRepo{
		getByIDUser: &User{ID: uid, Role: RoleUser, Status: StatusActive},
	}
	policy := &RiskActionPolicy{users: repo}
	event := &ConnectionRiskEvent{
		UserID:      &uid,
		SubjectType: ConnectionRiskSubjectUser,
		Severity:    connectionRiskSeverityCritical,
	}

	policy.HandleNewEvent(context.Background(), event, ConnectionRiskSettings{
		Phase:   connectionRiskPhaseObserve,
		Actions: ConnectionRiskActionSettings{AutoDisableEnabled: true},
	})
	require.Equal(t, 0, repo.updateCalls)

	policy.HandleNewEvent(context.Background(), event, ConnectionRiskSettings{
		Phase:   "enforce",
		Actions: ConnectionRiskActionSettings{AutoDisableEnabled: true},
	})
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, "disabled_user", event.ActionTaken)
}

func TestRiskActionPolicy_SoftThrottleRequiresEnabledAndHighSeverity(t *testing.T) {
	kid := int64(9)
	fake := &clearThrottleStub{}
	policy := &RiskActionPolicy{signals: fake}
	settings := ConnectionRiskSettings{
		Enabled: true,
		Phase:   connectionRiskPhaseSoftThrottle,
		Actions: ConnectionRiskActionSettings{SoftThrottleEnabled: true, ThrottleAbsRPM: 20},
	}
	low := &ConnectionRiskEvent{APIKeyID: &kid, Severity: connectionRiskSeverityLow}
	policy.HandleNewEvent(context.Background(), low, settings)
	require.Empty(t, fake.throttled)

	high := &ConnectionRiskEvent{APIKeyID: &kid, Severity: connectionRiskSeverityHigh}
	disabled := settings
	disabled.Enabled = false
	policy.HandleNewEvent(context.Background(), high, disabled)
	require.Empty(t, fake.throttled)

	policy.HandleNewEvent(context.Background(), high, settings)
	require.Equal(t, []int64{9}, fake.throttled)
	require.Equal(t, "throttled", high.ActionTaken)
}

func TestThrottleMinSeverityUnknownDefaultsHigh(t *testing.T) {
	require.Equal(t, connectionRiskSeverityHigh, throttleMinSeverity(ConnectionRiskSettings{}))
	require.Equal(t, connectionRiskSeverityHigh, throttleMinSeverity(ConnectionRiskSettings{
		Actions: ConnectionRiskActionSettings{ThrottleMinSeverity: "urgent"},
	}))
	require.Equal(t, connectionRiskSeverityMedium, throttleMinSeverity(ConnectionRiskSettings{
		Actions: ConnectionRiskActionSettings{ThrottleMinSeverity: "medium"},
	}))
	require.False(t, severityAtLeast(connectionRiskSeverityLow, throttleMinSeverity(ConnectionRiskSettings{
		Actions: ConnectionRiskActionSettings{ThrottleMinSeverity: "nope"},
	})))
}

func TestMergeWhitelistSamples(t *testing.T) {
	_, err := mergeWhitelistSamples(nil, []string{"2001:db8:1:2::"}, false)
	require.ErrorIs(t, err, ErrConnectionRiskWhitelistRestrictsAllowAll)

	merged, err := mergeWhitelistSamples(nil, []string{"2001:db8:1:2::", "192.0.2.10"}, true)
	require.NoError(t, err)
	require.Equal(t, []string{"2001:db8:1:2::/64", "192.0.2.10"}, merged)

	merged, err = mergeWhitelistSamples([]string{"10.0.0.1"}, []string{"2001:db8:1:2::"}, false)
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.1", "2001:db8:1:2::/64"}, merged)
}

func TestConnectionRiskService_SuppressEventSetsKeyExempt(t *testing.T) {
	kid := int64(9)
	fake := &clearThrottleStub{}
	svc := &ConnectionRiskService{
		signals: fake,
		events: &suppressEventRepoStub{ev: &ConnectionRiskEvent{ID: 3, APIKeyID: &kid}},
	}
	require.NoError(t, svc.SuppressEvent(context.Background(), 3, nil))
	require.Equal(t, []int64{9}, fake.cleared)
	require.Equal(t, []string{"k:9:suppressed"}, fake.exempts)
}

type suppressEventRepoStub struct {
	ev *ConnectionRiskEvent
}

func (s *suppressEventRepoStub) UpsertOpen(context.Context, *ConnectionRiskEvent) (*ConnectionRiskEvent, bool, error) {
	return nil, false, nil
}
func (s *suppressEventRepoStub) GetByID(context.Context, int64) (*ConnectionRiskEvent, error) {
	return s.ev, nil
}
func (s *suppressEventRepoStub) List(context.Context, *ConnectionRiskEventFilter) (*ConnectionRiskEventList, error) {
	return &ConnectionRiskEventList{}, nil
}
func (s *suppressEventRepoStub) UpdateStatus(context.Context, int64, string, *int64) error { return nil }
func (s *suppressEventRepoStub) UpdateActionTaken(context.Context, int64, string) error    { return nil }
func (s *suppressEventRepoStub) Delete(context.Context, int64) error                       { return nil }
func (s *suppressEventRepoStub) DeleteOlderThan(context.Context, time.Time) (int64, error) {
	return 0, nil
}
