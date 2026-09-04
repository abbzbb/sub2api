//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type resolveProxyRepoStub struct {
	proxy *Proxy
	err   error
}

func (s *resolveProxyRepoStub) GetByID(context.Context, int64) (*Proxy, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.proxy == nil {
		return nil, ErrProxyNotFound
	}
	copy := *s.proxy
	return &copy, nil
}

func TestResolveAccountProxyURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	proxyID := int64(9)
	groupID := int64(3)
	repo := &resolveProxyRepoStub{proxy: &Proxy{ID: proxyID, Protocol: "http", Host: "single.example", Port: 8080}}

	url, err := resolveAccountProxyURL(ctx, repo, nil)
	require.NoError(t, err)
	require.Empty(t, url)

	url, err = resolveAccountProxyURL(ctx, repo, &Account{
		Proxy: &Proxy{Protocol: "socks5", Host: "pool.example", Port: 1080},
	})
	require.NoError(t, err)
	require.Equal(t, "socks5://pool.example:1080", url)

	_, err = resolveAccountProxyURL(ctx, repo, &Account{ProxyGroupID: &groupID})
	require.Error(t, err)
	require.Equal(t, "PROXY_GROUP_NO_HEALTHY_MEMBER", infraerrors.Reason(err))

	_, err = resolveAccountProxyURL(ctx, repo, &Account{ProxyGroupExhausted: true})
	require.Error(t, err)
	require.Equal(t, "PROXY_GROUP_NO_HEALTHY_MEMBER", infraerrors.Reason(err))

	url, err = resolveAccountProxyURL(ctx, repo, &Account{ProxyID: &proxyID})
	require.NoError(t, err)
	require.Equal(t, "http://single.example:8080", url)

	_, err = resolveAccountProxyURL(ctx, nil, &Account{ProxyID: &proxyID})
	require.Error(t, err)
	require.Equal(t, "ACCOUNT_PROXY_UNAVAILABLE", infraerrors.Reason(err))

	placeholder := &Proxy{}
	url, err = resolveAccountProxyURL(ctx, nil, &Account{ProxyID: &proxyID, Proxy: placeholder})
	require.NoError(t, err)
	require.Equal(t, placeholder.URL(), url)
}

func TestRejectUnschedulableHydratedProxyAccount(t *testing.T) {
	t.Parallel()
	require.NoError(t, rejectUnschedulableHydratedProxyAccount(nil))
	require.NoError(t, rejectUnschedulableHydratedProxyAccount(&Account{ID: 1}))

	require.Error(t, rejectUnschedulableHydratedProxyAccount(&Account{ID: 2, ProxyGroupExhausted: true}))
	gid := int64(8)
	require.Error(t, rejectUnschedulableHydratedProxyAccount(&Account{ID: 3, ProxyGroupID: &gid}))
	require.NoError(t, rejectUnschedulableHydratedProxyAccount(&Account{
		ID:           4,
		ProxyGroupID: &gid,
		Proxy:        &Proxy{Host: "ok.example", Port: 1, Protocol: "http"},
	}))
}

func TestAccountProxyBindingConflict(t *testing.T) {
	t.Parallel()
	p, g := int64(1), int64(2)
	zero := int64(0)
	require.False(t, accountProxyBindingConflict(nil, nil))
	require.False(t, accountProxyBindingConflict(&p, nil))
	require.False(t, accountProxyBindingConflict(nil, &g))
	require.False(t, accountProxyBindingConflict(&p, &zero))
	require.True(t, accountProxyBindingConflict(&p, &g))
}

func TestLeftoverQuotaPathsFailClosedOnProxyGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	gid := int64(3)
	account := &Account{ProxyGroupID: &gid}

	_, err := NewAntigravityQuotaFetcher(nil, nil).GetProxyURL(ctx, account)
	require.Error(t, err)
	require.Equal(t, "PROXY_GROUP_NO_HEALTHY_MEMBER", infraerrors.Reason(err))

	_, err = (&CNProviderBalanceService{}).resolveProxyURL(ctx, account)
	require.Error(t, err)
	require.Equal(t, "PROXY_GROUP_NO_HEALTHY_MEMBER", infraerrors.Reason(err))

	_, err = (&CNProviderQuotaService{}).resolveProxyURL(ctx, account)
	require.Error(t, err)
	require.Equal(t, "PROXY_GROUP_NO_HEALTHY_MEMBER", infraerrors.Reason(err))
}

func TestResolveAccountProxyURL_RepoError(t *testing.T) {
	t.Parallel()
	proxyID := int64(4)
	_, err := resolveAccountProxyURL(context.Background(), &resolveProxyRepoStub{err: errors.New("db down")}, &Account{ProxyID: &proxyID})
	require.EqualError(t, err, "db down")
}
