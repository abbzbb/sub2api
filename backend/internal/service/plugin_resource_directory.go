package service

import (
	"context"
	"errors"
	"sort"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Resource access follows the same capability gate as the OAuth account directory.
// Proxy catalog entries and ResolveProxy are limited to proxies currently bound
// to accounts inside PluginAccountScope (direct proxy id, the hydrated proxy, or
// members of a bound proxy group). The catalog never carries tokens, usernames,
// passwords or authenticated URLs.
type PluginResourceDirectory interface {
	ListPluginResources(context.Context, PluginAccountScope) (*pluginv1.ListResourcesResponse, error)
	ResolvePluginProxy(context.Context, PluginAccountScope, int64) (string, error)
}
type pluginResourceDirectory struct {
	PluginAccountDirectory
	accounts AccountRepository
	proxies  ProxyRepository
}

func NewPluginResourceDirectory(base PluginAccountDirectory, accounts AccountRepository, proxies ProxyRepository) PluginAccountDirectory {
	return &pluginResourceDirectory{base, accounts, proxies}
}
func (d *pluginResourceDirectory) ListPluginResources(ctx context.Context, scope PluginAccountScope) (*pluginv1.ListResourcesResponse, error) {
	out := &pluginv1.ListResourcesResponse{ActionsSupported: true}
	if scope.Empty() {
		return out, nil
	}
	accounts, err := d.scopedAccounts(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		out.Accounts = append(out.Accounts, &pluginv1.AccountSummary{Id: a.ID, Name: a.Name})
	}
	proxies, err := d.proxiesForAccounts(ctx, accounts)
	if err != nil {
		return nil, err
	}
	for _, p := range proxies {
		out.Proxies = append(out.Proxies, &pluginv1.ProxySummary{Id: p.ID, Name: p.Name, Protocol: p.Protocol, Host: p.Host, Port: int32(p.Port)})
	}
	return out, nil
}
func (d *pluginResourceDirectory) ResolvePluginProxy(ctx context.Context, scope PluginAccountScope, id int64) (string, error) {
	if scope.Empty() || id <= 0 {
		return "", nil
	}
	accounts, err := d.scopedAccounts(ctx, scope)
	if err != nil {
		return "", err
	}
	if d == nil || d.proxies == nil {
		if !proxyDirectlyBound(accounts, id) && !accountHasProxyGroup(accounts) {
			return "", nil
		}
		return "", errors.New("proxy repository unavailable")
	}
	// A direct or hydrated binding is loaded by itself. Sibling bindings are
	// not queried, so a missing or failing unrelated proxy cannot fail this one.
	if proxyDirectlyBound(accounts, id) {
		return d.activeProxyURL(ctx, id)
	}
	for _, gid := range proxyGroupIDs(accounts) {
		members, err := d.proxies.ListByGroupID(ctx, gid)
		if err != nil {
			return "", err
		}
		for _, p := range members {
			if p.ID == id && p.IsActive() && !p.IsExpired(time.Now()) {
				return p.URL(), nil
			}
		}
	}
	return "", nil
}

func proxyDirectlyBound(accounts []Account, id int64) bool {
	for _, a := range accounts {
		if a.ProxyID != nil && *a.ProxyID == id {
			return true
		}
		if a.Proxy != nil && a.Proxy.ID == id {
			return true
		}
	}
	return false
}

func accountHasProxyGroup(accounts []Account) bool {
	return len(proxyGroupIDs(accounts)) > 0
}

func proxyGroupIDs(accounts []Account) []int64 {
	seen := map[int64]struct{}{}
	var out []int64
	for _, a := range accounts {
		if a.ProxyGroupID == nil || *a.ProxyGroupID <= 0 {
			continue
		}
		if _, ok := seen[*a.ProxyGroupID]; ok {
			continue
		}
		seen[*a.ProxyGroupID] = struct{}{}
		out = append(out, *a.ProxyGroupID)
	}
	return out
}

func (d *pluginResourceDirectory) activeProxyURL(ctx context.Context, id int64) (string, error) {
	p, err := d.lookupBoundProxy(ctx, id)
	if err != nil || p == nil || !p.IsActive() || p.IsExpired(time.Now()) {
		return "", err
	}
	return p.URL(), nil
}

// lookupBoundProxy treats a deleted or never-stored proxy as absent. Any other
// repository error is returned to the caller.
func (d *pluginResourceDirectory) lookupBoundProxy(ctx context.Context, id int64) (*Proxy, error) {
	p, err := d.proxies.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrProxyNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

// scopedAccounts returns the account rows whose current platform, type, and
// lifecycle still match the plugin scope. The directory listing and the binding
// snapshot are separate queries, so an ID from the first query is not enough:
// the second row is checked again for scope, the OpenAI OAuth filter, active
// status, and shadow exclusion. Paused accounts stay visible because they keep
// Status==active.
func (d *pluginResourceDirectory) scopedAccounts(ctx context.Context, scope PluginAccountScope) ([]Account, error) {
	if d == nil || d.accounts == nil {
		return nil, errors.New("account repository unavailable")
	}
	infos, err := d.ListPluginAccounts(ctx, scope, PlatformOpenAI, AccountTypeOAuth)
	if err != nil {
		return nil, err
	}
	allowed := make(map[int64]struct{}, len(infos))
	for _, info := range infos {
		allowed[info.ID] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, nil
	}
	accounts, err := d.accounts.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(allowed))
	for _, a := range accounts {
		if _, ok := allowed[a.ID]; !ok {
			continue
		}
		if !accountVisibleToPluginScope(scope, &a, PlatformOpenAI, AccountTypeOAuth) {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// accountVisibleToPluginScope re-validates a freshly loaded account. Schedulable
// is intentionally not required: a paused account remains Status==active.
func accountVisibleToPluginScope(scope PluginAccountScope, account *Account, platform, accountType string) bool {
	if account == nil || account.IsShadow() || !account.IsActive() {
		return false
	}
	if !scope.Contains(account.Platform, account.Type) {
		return false
	}
	if platform != "" && account.Platform != platform {
		return false
	}
	if accountType != "" && account.Type != accountType {
		return false
	}
	return true
}

// proxiesForAccounts returns active, unexpired proxies currently bound to the
// given accounts: ProxyID, the hydrated Proxy, and every member of ProxyGroupID.
func (d *pluginResourceDirectory) proxiesForAccounts(ctx context.Context, accounts []Account) ([]Proxy, error) {
	ids := make(map[int64]struct{})
	groups := make(map[int64]struct{})
	for _, a := range accounts {
		if a.ProxyID != nil && *a.ProxyID > 0 {
			ids[*a.ProxyID] = struct{}{}
		}
		if a.Proxy != nil && a.Proxy.ID > 0 {
			ids[a.Proxy.ID] = struct{}{}
		}
		if a.ProxyGroupID != nil && *a.ProxyGroupID > 0 {
			groups[*a.ProxyGroupID] = struct{}{}
		}
	}
	if len(ids) == 0 && len(groups) == 0 {
		return nil, nil
	}
	if d == nil || d.proxies == nil {
		return nil, errors.New("proxy repository unavailable")
	}
	byID := make(map[int64]Proxy)
	for gid := range groups {
		members, err := d.proxies.ListByGroupID(ctx, gid)
		if err != nil {
			return nil, err
		}
		for _, p := range members {
			if p.ID > 0 {
				byID[p.ID] = p
			}
		}
	}
	for id := range ids {
		if _, ok := byID[id]; ok {
			continue
		}
		p, err := d.lookupBoundProxy(ctx, id)
		if err != nil {
			return nil, err
		}
		if p != nil && p.ID > 0 {
			byID[p.ID] = *p
		}
	}
	now := time.Now()
	out := make([]Proxy, 0, len(byID))
	for _, p := range byID {
		if p.IsActive() && !p.IsExpired(now) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *pluginHostServiceServer) ListResources(ctx context.Context, req *pluginv1.ListResourcesRequest) (*pluginv1.ListResourcesResponse, error) {
	if s == nil || s.directory == nil {
		return nil, status.Error(codes.PermissionDenied, "resource directory unavailable")
	}
	d, ok := s.directory.(PluginResourceDirectory)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "resource directory not supported")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	out, err := d.ListPluginResources(ctx, s.scope)
	if err != nil {
		return nil, status.Error(codes.Internal, "resource directory unavailable")
	}
	return out, nil
}
func (s *pluginHostServiceServer) ResolveProxy(ctx context.Context, req *pluginv1.ResolveProxyRequest) (*pluginv1.ResolveProxyResponse, error) {
	if s == nil || s.directory == nil {
		return nil, status.Error(codes.PermissionDenied, "proxy directory unavailable")
	}
	d, ok := s.directory.(PluginResourceDirectory)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "proxy directory not supported")
	}
	if req == nil || req.ProxyId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid proxy id")
	}
	u, err := d.ResolvePluginProxy(ctx, s.scope, req.ProxyId)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "selected proxy unavailable")
	}
	return &pluginv1.ResolveProxyResponse{Found: u != "", ProxyUrl: u}, nil
}
