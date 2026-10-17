//go:build unit && integration

package service

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestRefundPostgresRenewalDuringFailureQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	image := os.Getenv("SUB2API_TEST_POSTGRES_IMAGE")
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	container, err := tcpostgres.Run(ctx, image, tcpostgres.WithDatabase("refund_renewal"),
		tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	sqlDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, sqlDB)))
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Schema.Create(ctx))
	migration, err := migrations.FS.ReadFile("288_payment_refund_hold_fence.sql")
	require.NoError(t, err)
	_, err = sqlDB.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	db, svc, plan, prov := heldRefundFixtureWithClient(t, client)
	_, _, _, _, subSvc, sub, now := addRefundTestSubscription(t, db, svc, plan, prov)
	_, err = svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	queryStarted := make(chan struct{})
	finishQuery := make(chan struct{})
	prov.queryStatus = payment.ProviderStatusFailed
	prov.onQuery = func() {
		close(queryStarted)
		select {
		case <-finishQuery:
		case <-ctx.Done():
		}
	}
	done := make(chan error, 1)
	go func() { _, err := svc.QueryAndFinalizeRefund(ctx, plan.OrderID); done <- err }()
	select {
	case <-queryStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, renewErr := subSvc.ExtendSubscription(ctx, sub.ID, 40)
	close(finishQuery)
	require.NoError(t, renewErr)
	select {
	case err = <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	final, err := db.UserSubscription.Get(ctx, sub.ID)
	require.NoError(t, err)
	require.True(t, final.ExpiresAt.Equal(now.AddDate(0, 0, 60)), "expiry = %s", final.ExpiresAt)
	o, err := db.PaymentOrder.Get(ctx, plan.OrderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, o.Status)
	require.False(t, o.RefundState.DeductionHeld)
}
