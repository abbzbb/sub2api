package repository

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestClearModelRateLimitIfUnchangedUsesAtomicKeyDeletion(t *testing.T) {
	for _, outcome := range []string{"cleared", "new_generation", "outbox_failure"} {
		t.Run(outcome, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			repo := newAccountRepositoryWithSQL(client, db, nil)
			expected := map[string]any{"rate_limit_reset_at": "2026-10-18T01:00:00Z"}
			query := mock.ExpectExec(`(?s)WITH updated AS.*UPDATE accounts.*extra = extra #- ARRAY\['model_rate_limits', \$2\]::text\[\].*extra #> ARRAY\['model_rate_limits', \$2\]::text\[\] = \$3::jsonb.*INSERT INTO scheduler_outbox.*SELECT \$4, updated.id`).
				WithArgs(int64(42), "AICredits", `{"rate_limit_reset_at":"2026-10-18T01:00:00Z"}`, service.SchedulerOutboxEventAccountChanged)
			if outcome == "outbox_failure" {
				query.WillReturnError(errors.New("outbox insert failed"))
			} else {
				rows := int64(1)
				if outcome == "new_generation" {
					rows = 0
				}
				query.WillReturnResult(sqlmock.NewResult(0, rows))
			}
			cleared, err := repo.ClearModelRateLimitIfUnchanged(context.Background(), 42, "AICredits", expected)
			require.Equal(t, outcome == "cleared", cleared)
			if outcome == "outbox_failure" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
