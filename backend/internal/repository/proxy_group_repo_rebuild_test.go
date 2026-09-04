package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProxyGroupEnqueueBoundAccountsRebuildEmitsBulkOutbox(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := newProxyGroupRepositoryWithSQL(nil, db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM accounts WHERE proxy_group_id=$1 AND deleted_at IS NULL ORDER BY id")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(17)).AddRow(int64(18)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)")).
		WithArgs(service.SchedulerOutboxEventAccountBulkChanged, nil, nil, accountIDsPayloadMatcher{want: []int64{17, 18}}).
		WillReturnResult(sqlmock.NewResult(1, 1))

	n, err := repo.EnqueueBoundAccountsRebuild(context.Background(), 9)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProxyGroupEnqueueBoundAccountsRebuildSkipsWhenNoAccounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := newProxyGroupRepositoryWithSQL(nil, db)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM accounts WHERE proxy_group_id=$1")).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	n, err := repo.EnqueueBoundAccountsRebuild(context.Background(), 9)
	require.NoError(t, err)
	require.Equal(t, 0, n)
	require.NoError(t, mock.ExpectationsWereMet())
}
