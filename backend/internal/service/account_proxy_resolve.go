package service

import (
	"context"
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// resolveAccountProxyURL 是出站代理解析的统一入口。
// 已 hydrate 的 Proxy（含代理组选出的成员）优先；绑定了代理组但没有成员时 fail-closed，
// 禁止静默直连本机出口；否则回退按 ProxyID 查库。
type accountProxyLookup interface {
	GetByID(ctx context.Context, id int64) (*Proxy, error)
}

func resolveAccountProxyURL(ctx context.Context, proxyRepo accountProxyLookup, account *Account) (string, error) {
	if account == nil {
		return "", nil
	}
	if account.Proxy != nil && strings.TrimSpace(account.Proxy.Host) != "" {
		return account.ProxyURL(), nil
	}
	if account.ProxyGroupID != nil || account.ProxyGroupExhausted {
		return "", ErrProxyGroupNoHealthyMember
	}
	if account.ProxyID == nil {
		return "", nil
	}
	if proxyRepo == nil {
		// 占位 Proxy（无 host）表示绑定已配置，eligibility 放行，真正查库留给带 repo 的刷新路径。
		if account.Proxy != nil {
			return account.ProxyURL(), nil
		}
		return "", infraerrors.New(http.StatusServiceUnavailable, "ACCOUNT_PROXY_UNAVAILABLE", "proxy repository is not available")
	}
	proxy, err := proxyRepo.GetByID(ctx, *account.ProxyID)
	if err != nil {
		return "", err
	}
	if proxy == nil {
		return "", ErrProxyNotFound
	}
	return proxy.URL(), nil
}

// rejectUnschedulableHydratedProxyAccount 在调度选中账号 hydrate 之后复查代理组。
// 缓存命中路径可能只带 ProxyGroupID / ProxyGroupExhausted，完整 payload 仍可能 Proxy==nil。
func rejectUnschedulableHydratedProxyAccount(account *Account) error {
	if account == nil {
		return nil
	}
	if account.ProxyGroupExhausted {
		return ErrProxyGroupNoHealthyMember
	}
	if account.ProxyGroupID != nil && (account.Proxy == nil || strings.TrimSpace(account.Proxy.Host) == "") {
		return ErrProxyGroupNoHealthyMember
	}
	return nil
}

func accountProxyBindingConflict(proxyID, proxyGroupID *int64) bool {
	return proxyID != nil && *proxyID > 0 && proxyGroupID != nil && *proxyGroupID > 0
}
