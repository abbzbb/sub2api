//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type blockingCRSignals struct {
	clearThrottleStub
	started chan struct{}
}

func (s *blockingCRSignals) PruneActive(ctx context.Context, _ int, _ time.Duration) error {
	select {
	case <-s.started:
	default:
		close(s.started)
	}
	<-ctx.Done()
	return ctx.Err()
}

type noopCREvents struct{}

func (noopCREvents) UpsertOpen(context.Context, *ConnectionRiskEvent) (*ConnectionRiskEvent, bool, error) {
	return nil, false, nil
}
func (noopCREvents) GetByID(context.Context, int64) (*ConnectionRiskEvent, error) { return nil, nil }
func (noopCREvents) List(context.Context, *ConnectionRiskEventFilter) (*ConnectionRiskEventList, error) {
	return &ConnectionRiskEventList{}, nil
}
func (noopCREvents) UpdateStatus(context.Context, int64, string, *int64) error { return nil }
func (noopCREvents) UpdateActionTaken(context.Context, int64, string) error    { return nil }
func (noopCREvents) Delete(context.Context, int64) error                       { return nil }
func (noopCREvents) DeleteOlderThan(context.Context, time.Time) (int64, error) { return 0, nil }

func TestConnectionRiskWorkerStopCancelsEvaluateOnce(t *testing.T) {
	enabled := DefaultConnectionRiskSettings()
	enabled.Enabled = true
	data, err := json.Marshal(enabled)
	require.NoError(t, err)
	settings := NewSettingService(&connectionRiskSettingRepo{
		values: map[string]string{SettingKeyConnectionRiskSettings: string(data)},
	}, nil)

	signals := &blockingCRSignals{started: make(chan struct{})}
	w := NewConnectionRiskWorker(
		&config.Config{ConnectionRisk: config.ConnectionRiskConfig{Enabled: true}},
		settings,
		signals,
		noopCREvents{},
		nil,
		nil,
		nil,
		nil,
		&ConnectionRiskMetrics{},
		nil,
	)
	w.runCtx, w.runCancel = context.WithCancel(context.Background())
	w.started = true

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.evaluateOnce()
	}()
	select {
	case <-signals.started:
	case <-time.After(2 * time.Second):
		t.Fatal("evaluateOnce did not reach in-flight work")
	}
	start := time.Now()
	w.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("evaluateOnce was not cancelled by Stop")
	}
	require.Less(t, time.Since(start), 2*time.Second)
}

type recordingCREvents struct {
	noopCREvents
	events  []*ConnectionRiskEvent
	created []bool
}

func (r *recordingCREvents) UpsertOpen(_ context.Context, ev *ConnectionRiskEvent) (*ConnectionRiskEvent, bool, error) {
	cp := *ev
	created := len(r.events) == 0
	r.events = append(r.events, &cp)
	r.created = append(r.created, created)
	cp.ID = int64(len(r.events))
	return &cp, created, nil
}

type scoreKeySignals struct {
	clearThrottleStub
}

func (s *scoreKeySignals) ReadKeyWindowMetrics(context.Context, int64, int64, int64) (*ConnectionRiskSubjectMetrics, error) {
	return &ConnectionRiskSubjectMetrics{APIKeyID: 7, DistinctIP5m: 8, ReqCount5m: 20}, nil
}

func TestConnectionRiskWorkerScoreKeyDedupeAndCreatedOnlyPolicy(t *testing.T) {
	settings := *DefaultConnectionRiskSettings()
	settings.Enabled = true
	settings.Phase = connectionRiskPhaseSoftThrottle
	settings.Actions.SoftThrottleEnabled = true
	settings.Actions.ThrottleMinSeverity = connectionRiskSeverityLow

	signals := &scoreKeySignals{}
	events := &recordingCREvents{}
	w := NewConnectionRiskWorker(
		&config.Config{},
		nil,
		signals,
		events,
		nil,
		nil,
		nil,
		nil,
		&ConnectionRiskMetrics{},
		&RiskActionPolicy{signals: signals},
	)
	now := time.Now().Unix()
	w.scoreKey(context.Background(), 7, now, settings)
	w.scoreKey(context.Background(), 7, now, settings)
	require.Len(t, events.events, 2)
	require.Equal(t, "k:7:R1", events.events[0].DedupeKey)
	require.Equal(t, "k:7:R1", events.events[1].DedupeKey)
	require.True(t, events.created[0])
	require.False(t, events.created[1])
	require.Equal(t, []int64{7}, signals.throttled)
}
