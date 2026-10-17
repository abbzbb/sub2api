//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestPaymentRefundHoldFence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	image := os.Getenv("SUB2API_TEST_POSTGRES_IMAGE")
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	container, err := tcpostgres.Run(ctx, image, tcpostgres.WithDatabase("refund_fence"),
		tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, `CREATE TABLE payment_orders (id bigint PRIMARY KEY, status text, refund_state jsonb);
INSERT INTO payment_orders VALUES
 (1, 'COMPLETED', '{"version":2,"deduction_held":true,"provider_status":"pending","attempt_id":"reserved"}'),
 (2, 'COMPLETED', '{"version":1,"provider_status":"pending"}'),
 (3, 'COMPLETED', NULL),
 (4, 'REFUND_FAILED', '{"version":2,"deduction_held":false,"provider_status":"failed"}');`)
	require.NoError(t, err)
	migration, err := FS.ReadFile("288_payment_refund_hold_fence.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = db.ExecContext(ctx, string(migration))
		require.NoError(t, err, "migration must be idempotent")
	}
	var status string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT status FROM payment_orders WHERE id=1").Scan(&status))
	require.Equal(t, "REFUND_PENDING", status)
	for _, id := range []int{2, 3} {
		require.NoError(t, db.QueryRowContext(ctx, "SELECT status FROM payment_orders WHERE id=$1", id).Scan(&status))
		require.Equal(t, "COMPLETED", status, "legacy operations must not be relabelled")
	}
	// Replay the old executor's initial status claim: it cannot claim a hold.
	res, err := db.ExecContext(ctx, `UPDATE payment_orders SET status='REFUNDING' WHERE id=1
AND status IN ('COMPLETED','REFUND_REQUESTED','REFUND_FAILED')`)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.Zero(t, n)
	// Even a stale executor that already claimed the order cannot replace a hold
	// with freshly constructed v1 state or clear it outright.
	for _, state := range []string{`{"version":1,"provider_status":"pending"}`, `null`, `{}`} {
		_, err = db.ExecContext(ctx, "UPDATE payment_orders SET refund_state=$1::jsonb WHERE id=1", state)
		require.ErrorContains(t, err, "reserved refund state cannot be downgraded")
	}
	_, err = db.ExecContext(ctx, "UPDATE payment_orders SET refund_state=NULL WHERE id=1")
	require.ErrorContains(t, err, "reserved refund state cannot be downgraded")
	// Current releases and subsequent independent attempts remain valid.
	_, err = db.ExecContext(ctx, `UPDATE payment_orders SET status='REFUND_FAILED',
refund_state='{"version":2,"deduction_held":false,"provider_status":"failed"}' WHERE id=1;
UPDATE payment_orders SET refund_state='{"version":1,"provider_status":"pending"}' WHERE id=1;`)
	require.NoError(t, err)
}
