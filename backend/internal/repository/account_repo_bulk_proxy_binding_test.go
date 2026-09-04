package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func int64Ptr(v int64) *int64 { return &v }

// 同时携带 proxy_id 与 proxy_group_id 时，每列最多只能出现一次赋值，
// 否则 Postgres 报 "multiple assignments to same column"。
func TestBulkUpdateProxyBindingEmitsEachColumnOnce(t *testing.T) {
	cases := []struct {
		name        string
		proxyID     *int64
		groupID     *int64
		wantProxy   string
		wantGroup   string
		wantErr     error
		wantArgsLen int
	}{
		{"clear proxy, set group", int64Ptr(0), int64Ptr(9), "proxy_id = NULL", "proxy_group_id = $1", nil, 1},
		{"set proxy, clear group", int64Ptr(5), int64Ptr(0), "proxy_id = $1", "proxy_group_id = NULL", nil, 1},
		{"clear both", int64Ptr(0), int64Ptr(0), "proxy_id = NULL", "proxy_group_id = NULL", nil, 0},
		{"set proxy only", int64Ptr(5), nil, "proxy_id = $1", "proxy_group_id = NULL", nil, 1},
		{"set group only", nil, int64Ptr(9), "proxy_id = NULL", "proxy_group_id = $1", nil, 1},
		{"both non-zero conflicts", int64Ptr(5), int64Ptr(9), "", "", service.ErrAccountProxyBindingConflict, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
			repo := newAccountRepositoryWithSQL(nil, exec, nil)

			_, err := repo.BulkUpdate(context.Background(), []int64{17}, service.AccountBulkUpdate{
				ProxyID:      tc.proxyID,
				ProxyGroupID: tc.groupID,
			})
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				require.Empty(t, exec.execQueries)
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, exec.execQueries)
			query := normalizeSQLWhitespace(exec.execQueries[0])
			require.Equal(t, 1, strings.Count(query, "proxy_id ="), query)
			require.Equal(t, 1, strings.Count(query, "proxy_group_id ="), query)
			require.Contains(t, query, tc.wantProxy)
			require.Contains(t, query, tc.wantGroup)
		})
	}
}
