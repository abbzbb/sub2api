package repository

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 占位符数量必须与参数一一对应，且每个 $N 恰好出现一次。
// 该语句曾因缺逗号（"NOW() health_fail_count"）、onlyIfIsolatedBy 后 argN 未自增、
// 以及引用不存在的 generation 列而在生产中静默失效（recover 永远报错）。
func TestBuildUpdateStatusWithHealthIsolationQuery_PlaceholdersMatchArgs(t *testing.T) {
	iso := "health"
	now := time.Now()
	cases := []struct {
		name                 string
		onlyIfStatus         string
		onlyIfIsolatedBy     *string
		updateHealthCounters bool
		wantArgs             int
	}{
		{"isolate: status only + status guard", "active", nil, false, 3},
		{"recover: counters + status guard + isolated_by guard", "inactive", &iso, true, 7},
		{"counters only", "", nil, true, 5},
		{"no guards, no counters", "", nil, false, 2},
		{"isolated_by guard without status guard", "", &iso, true, 6},
	}
	placeholder := regexp.MustCompile(`\$(\d+)`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query, args := buildUpdateStatusWithHealthIsolationQuery(7, "inactive", 0, now, "health", tc.onlyIfStatus, tc.onlyIfIsolatedBy, tc.updateHealthCounters)
			require.Len(t, args, tc.wantArgs)

			seen := map[string]int{}
			for _, m := range placeholder.FindAllStringSubmatch(query, -1) {
				seen[m[1]]++
			}
			require.Len(t, seen, tc.wantArgs, "distinct placeholders must equal args: %s", query)
			for i := 1; i <= tc.wantArgs; i++ {
				require.Equal(t, 1, seen[itoa(i)], "placeholder $%d must appear exactly once: %s", i, query)
			}

			require.NotContains(t, query, "generation", "proxies has no generation column")
			require.NotContains(t, query, "NOW() health_fail_count", "SET clauses must be comma separated")
			require.Regexp(t, `SET status = \$2, updated_at = NOW\(\)`, query)
			if tc.updateHealthCounters {
				require.Regexp(t, `updated_at = NOW\(\), health_fail_count = \$3, last_health_at = \$4, health_isolated_by = \$5`, query)
			}
			if tc.onlyIfStatus != "" {
				require.True(t, strings.Contains(query, "AND status = $"), query)
			}
			if tc.onlyIfIsolatedBy != nil {
				require.True(t, strings.Contains(query, "COALESCE(health_isolated_by,'') = $"), query)
			}
		})
	}
}
