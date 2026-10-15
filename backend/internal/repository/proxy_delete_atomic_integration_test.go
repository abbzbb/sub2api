//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProxyDeleteFailurePreservesFallbackAndBackupBindings(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newProxyRepositoryWithSQL(client, integrationDB)
	target := &service.Proxy{Name: "atomic-delete-target", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: service.StatusActive}
	require.NoError(t, repo.Create(ctx, target))
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM proxies WHERE id = $1`, target.ID)
		require.NoError(t, err)
	})
	backupUser := &service.Proxy{Name: "atomic-delete-backup-user", Protocol: "http", Host: "127.0.0.1", Port: 8081, Status: service.StatusActive, BackupProxyID: &target.ID, FallbackMode: service.FallbackModeProxy}
	require.NoError(t, repo.Create(ctx, backupUser))
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM proxies WHERE id = $1`, backupUser.ID)
		require.NoError(t, err)
	})
	account := mustCreateAccount(t, client, &service.Account{Name: "atomic-delete-origin-user"})
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
		require.NoError(t, err)
	})
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET proxy_fallback_origin_id = $1 WHERE id = $2`, target.ID, account.ID)
	require.NoError(t, err)
	count, err := repo.CountAccountsByProxyID(ctx, target.ID)
	require.NoError(t, err)
	require.Zero(t, count, "no primary bindings, so admin deletion is allowed")

	// This client shares the test DB but owns its hooks. Do not close it:
	// closing the SQL driver would close the suite's shared connection pool.
	failingClient := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
	deleteErr := errors.New("injected proxy deletion failure after binding cleanup")
	failingClient.Proxy.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) { return nil, deleteErr })
	})
	err = newProxyRepositoryWithSQL(failingClient, integrationDB).Delete(ctx, target.ID)
	require.ErrorIs(t, err, deleteErr)
	var origin, backup sql.NullInt64
	readBindings := func() {
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT proxy_fallback_origin_id FROM accounts WHERE id = $1`, account.ID).Scan(&origin))
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT backup_proxy_id FROM proxies WHERE id = $1`, backupUser.ID).Scan(&backup))
	}
	readBindings()
	require.Equal(t, sql.NullInt64{Int64: target.ID, Valid: true}, origin)
	require.Equal(t, sql.NullInt64{Int64: target.ID, Valid: true}, backup)
	_, err = repo.GetByID(ctx, target.ID)
	require.NoError(t, err, "failed deletion must retain the proxy row")

	require.NoError(t, repo.Delete(ctx, target.ID))
	readBindings()
	require.False(t, origin.Valid)
	require.False(t, backup.Valid)
	_, err = repo.GetByID(ctx, target.ID)
	require.ErrorIs(t, err, service.ErrProxyNotFound)
}
