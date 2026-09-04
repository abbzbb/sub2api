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
