//go:build unit

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCaptureBatchImageBalance_KeySpendingIsAtomicAndIdempotent(t *testing.T) {
	for _, scenario := range []string{"success", "duplicate", "quota failure", "rate limit failure", "deleted key", "zero cost"} {
		t.Run(scenario, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			repo := &usageBillingRepository{db: db}
			cmd := &service.BatchImageBalanceHoldCommand{
				RequestID: "batch_image_capture:test", APIKeyID: 7, UserID: 42,
				BatchID: "test", HoldAmount: 1, ActualAmount: 0.25,
			}
			if scenario == "zero cost" {
				cmd.ActualAmount = 0
			}
			cmd.Normalize()
			mock.ExpectBegin()
			claim := mock.ExpectQuery(`INSERT INTO usage_billing_dedup`).
				WithArgs(cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint)
			if scenario == "duplicate" {
				claim.WillReturnRows(sqlmock.NewRows([]string{"id"}))
				mock.ExpectQuery(`SELECT request_fingerprint\s+FROM usage_billing_dedup`).
					WithArgs(cmd.RequestID, cmd.APIKeyID).
					WillReturnRows(sqlmock.NewRows([]string{"request_fingerprint"}).AddRow(cmd.RequestFingerprint))
				mock.ExpectRollback()
				result, err := repo.CaptureBatchImageBalance(context.Background(), cmd)
				require.NoError(t, err)
				require.False(t, result.Applied)
				require.NoError(t, mock.ExpectationsWereMet())
				return
			}
			claim.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
			mock.ExpectQuery(`SELECT request_fingerprint\s+FROM usage_billing_dedup_archive`).
				WithArgs(cmd.RequestID, cmd.APIKeyID).
				WillReturnRows(sqlmock.NewRows([]string{"request_fingerprint"}))
			mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM batch_image_money_holds WHERE batch_id=\$1 AND api_key_id=\$2\)`).
				WithArgs(cmd.BatchID, cmd.APIKeyID).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			mock.ExpectQuery(captureBatchImageHoldSQL).
				WithArgs(cmd.HoldAmount, cmd.ActualAmount, cmd.UserID).
				WillReturnRows(sqlmock.NewRows([]string{"balance", "frozen_balance"}).AddRow(9.75, 0))
			writeErr := errors.New("key spending update failed")
			if cmd.ActualAmount > 0 {
				quota := mock.ExpectQuery(apiKeyQuotaIncrementSQL).
					WithArgs(cmd.ActualAmount, cmd.APIKeyID, service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted)
				switch scenario {
				case "quota failure":
					quota.WillReturnError(writeErr)
				case "deleted key":
					quota.WillReturnError(sql.ErrNoRows)
				default:
					quota.WillReturnRows(sqlmock.NewRows([]string{"exhausted"}).AddRow(true))
				}
				if scenario != "quota failure" {
					rate := mock.ExpectExec(apiKeyRateLimitIncrementSQL).WithArgs(cmd.ActualAmount, cmd.APIKeyID)
					switch scenario {
					case "rate limit failure":
						rate.WillReturnError(writeErr)
					case "deleted key":
						rate.WillReturnResult(sqlmock.NewResult(0, 0))
					default:
						rate.WillReturnResult(sqlmock.NewResult(0, 1))
					}
				}
			}
			wantError := scenario == "quota failure" || scenario == "rate limit failure"
			if wantError {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			result, err := repo.CaptureBatchImageBalance(context.Background(), cmd)
			if wantError {
				require.ErrorIs(t, err, writeErr)
				require.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.True(t, result.Applied)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
