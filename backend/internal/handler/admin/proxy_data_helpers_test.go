package admin

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type staticProxyLookup struct {
	proxies []service.Proxy
}

func (s staticProxyLookup) GetProxiesByIDs(_ context.Context, ids []int64) ([]service.Proxy, error) {
	want := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := make([]service.Proxy, 0, len(ids))
	for i := range s.proxies {
		if _, ok := want[s.proxies[i].ID]; ok {
			out = append(out, s.proxies[i])
		}
	}
	return out, nil
}

func TestExpandBackupProxyChainIncludesTransitiveAndStopsCycles(t *testing.T) {
	bID := int64(2)
	cID := int64(3)
	lookup := staticProxyLookup{proxies: []service.Proxy{
		{ID: 1, Name: "a", BackupProxyID: &bID},
		{ID: 2, Name: "b", BackupProxyID: &cID},
		{ID: 3, Name: "c", BackupProxyID: &bID},
	}}
	got, err := expandBackupProxyChain(context.Background(), lookup, []service.Proxy{lookup.proxies[0]})
	require.NoError(t, err)
	require.Len(t, got, 3)
	names := make([]string, 0, len(got))
	for _, p := range got {
		names = append(names, p.Name)
	}
	require.ElementsMatch(t, []string{"a", "b", "c"}, names)
}

func TestExpandBackupProxyChainMissingBackupErrors(t *testing.T) {
	missing := int64(99)
	_, err := expandBackupProxyChain(context.Background(), staticProxyLookup{}, []service.Proxy{
		{ID: 1, Name: "primary", BackupProxyID: &missing},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing backup proxy id 99")
}
