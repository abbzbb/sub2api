package requesttiming

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func latencyAttempt(number int, account int64, start, end float64) Attempt {
	return Attempt{Kind: "http", Number: number, AccountID: account, StartMS: start, EndMS: &end, RequestBytes: 2 << 20, Events: map[string]float64{"headers_written": start + 10, "request_written": start + 40, "first_semantic": start + 100, "first_visible": start + 150, "response_headers": start + 90}}
}

// Marshaling the existing collector is a production-path RED counterexample:
// old collector source compiles but does not publish the derived JSON contract.
func TestIssue603CollectorPublishesAttemptMetrics(t *testing.T) {
	c := New(time.Now(), 0)
	c.data.Attempts = []Attempt{latencyAttempt(1, 1, 0, 200)}
	c.Finish(200, false)
	c.WhenFinished(func(s Snapshot) {
		raw, err := json.Marshal(s)
		require.NoError(t, err)
		var doc map[string]any
		require.NoError(t, json.Unmarshal(raw, &doc))
		require.Contains(t, doc, "latency", "finished collector must publish attempt-derived observations")
	})
}

func TestIssue603AttemptBoundaryAndFailoverSemantics(t *testing.T) {
	s := Snapshot{BodyBytes: 2 << 20, Attempts: []Attempt{latencyAttempt(1, 1, 0, 200), latencyAttempt(2, 1, 220, 420), latencyAttempt(3, 2, 440, 640)}}
	s.Events = map[string]float64{"first_semantic": 1}
	s.Attempts = append([]Attempt{{Kind: "egress", AccountID: 99}}, s.Attempts...)
	got := DeriveLatency(s)
	require.Len(t, got.Attempts, 3)
	require.Equal(t, 1, got.AccountSwitches)
	require.Equal(t, 1, got.SameAccountRetries)
	require.Equal(t, 30.0, *got.Attempts[0].Upload.MS)
	require.Equal(t, 60.0, *got.Attempts[0].SemanticWait.MS)
	require.Equal(t, 110.0, *got.Attempts[0].VisibleWait.MS)
	require.Equal(t, 60.0, *got.ExtraUploadComponentMS)
	require.Equal(t, "measured_components", got.CostStatus)
	for _, tc := range []struct {
		name   string
		modify func(*Attempt)
		status string
	}{
		{"missing", func(a *Attempt) { delete(a.Events, "request_written") }, "not_collected"},
		{"reversed", func(a *Attempt) { a.Events["request_written"] = 0 }, "invalid_boundary"},
		{"failed write", func(a *Attempt) { a.Events["request_write_failed"] = 30 }, "write_failed"},
		{"unfinished", func(a *Attempt) { a.EndMS = nil }, "invalid_boundary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := latencyAttempt(1, 1, 0, 200)
			tc.modify(&a)
			v := DeriveLatency(Snapshot{Attempts: []Attempt{a}})
			require.Nil(t, v.Attempts[0].Upload.MS)
			require.Equal(t, tc.status, v.Attempts[0].Upload.Status)
			require.Nil(t, v.Attempts[0].SemanticWait.MS)
		})
	}
	a := latencyAttempt(1, 1, 0, 200)
	a.Events["request_written"] = a.Events["headers_written"]
	require.Equal(t, 0.0, *DeriveLatency(Snapshot{Attempts: []Attempt{a}}).Attempts[0].Upload.MS, "measured zero must survive")
	s.Attempts[2].StartMS = 100
	require.Equal(t, "overlap", DeriveLatency(s).CostStatus)
	require.Nil(t, DeriveLatency(s).ExtraUploadComponentMS)
	s.Truncated = true
	require.Equal(t, "partial", DeriveLatency(s).Status)
}
