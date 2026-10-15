package repository

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestProxyDeleteBindingsAndRowShareTransaction(t *testing.T) {
	queries := []string{
		`UPDATE accounts\s+SET proxy_id = NULL`,
		`UPDATE accounts\s+SET proxy_fallback_origin_id = NULL`,
		`UPDATE proxies\s+SET backup_proxy_id = NULL`,
		`UPDATE "proxies" SET`,
	}
	for _, failAt := range []int{-1, 0, 1, 2, 3, 4} {
		t.Run([]string{"success", "primary cleanup", "origin cleanup", "backup cleanup", "delete", "commit"}[failAt+1], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			repo := newProxyRepositoryWithSQL(client, db)
			writeErr := errors.New("injected deletion failure")
			mock.ExpectBegin()
			for i, query := range queries {
				expected := mock.ExpectExec(query)
				if i < 3 {
					expected.WithArgs(int64(7))
				}
				if i == failAt {
					expected.WillReturnError(writeErr)
					break
				}
				expected.WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if failAt >= 0 && failAt < 4 {
				mock.ExpectRollback()
			} else if failAt == 4 {
				mock.ExpectCommit().WillReturnError(writeErr)
			} else {
				mock.ExpectCommit()
			}
			err = repo.Delete(context.Background(), 7)
			if failAt >= 0 {
				require.ErrorIs(t, err, writeErr)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
