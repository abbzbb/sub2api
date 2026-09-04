//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 真库回归：同一 dedupe 桶内 ack 之后信号持续，UpsertOpen 必须刷新已 acknowledged 的行，
// 而不是再插入一条新的 open 事件；resolved 之后则应重新建事件。
func TestConnectionRiskUpsertOpenRefreshesAcknowledgedRow(t *testing.T) {
	ctx := context.Background()
	repo := NewConnectionRiskEventRepository(integrationDB)
	dedupe := "k:9001:it-ack:" + time.Now().UTC().Format("20060102150405.000000000")
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM connection_risk_events WHERE dedupe_key = $1", dedupe)
	})

	newEvent := func(score float64) *service.ConnectionRiskEvent {
		return &service.ConnectionRiskEvent{
			SubjectType: service.ConnectionRiskSubjectAPIKey,
			RulesFired:  []service.ConnectionRiskRuleHit{{RuleID: "it-ack", Severity: "medium"}},
			Severity:    "medium",
			Score:       score,
			Status:      service.ConnectionRiskStatusOpen,
			Title:       "integration",
			Summary:     "integration",
			DedupeKey:   dedupe,
		}
	}

	first, err := repo.UpsertOpen(ctx, newEvent(10))
	require.NoError(t, err)
	require.Equal(t, service.ConnectionRiskStatusOpen, first.Status)

	require.NoError(t, repo.UpdateStatus(ctx, first.ID, service.ConnectionRiskStatusAcknowledged, nil))

	second, err := repo.UpsertOpen(ctx, newEvent(20))
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID, "continuing signal must refresh the acknowledged row")
	require.Equal(t, service.ConnectionRiskStatusAcknowledged, second.Status, "refresh must not reopen an acknowledged event")
	require.Equal(t, 20.0, second.Score)

	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM connection_risk_events WHERE dedupe_key = $1", dedupe).Scan(&count))
	require.Equal(t, 1, count)

	require.NoError(t, repo.UpdateStatus(ctx, first.ID, service.ConnectionRiskStatusResolved, nil))

	third, err := repo.UpsertOpen(ctx, newEvent(5))
	require.NoError(t, err)
	require.NotEqual(t, first.ID, third.ID, "a recurrence after resolve must open a new event")
	require.Equal(t, service.ConnectionRiskStatusOpen, third.Status)
}
