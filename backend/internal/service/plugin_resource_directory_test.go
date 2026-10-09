package service

import (
	"context"
	"encoding/json"
	"errors"
	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
	"time"
)

type resourceAccounts struct {
	AccountRepository
	rows []Account
}

func (r *resourceAccounts) ListByPlatform(context.Context, string) ([]Account, error) {
	if r != nil && r.rows != nil {
		return r.rows, nil
	}
	bound := int64(3)
	other := int64(9)
	return []Account{
		{ID: 7, Name: "Mail Pro", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, ProxyID: &bound},
		{ID: 8, Name: "Hidden", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, ProxyID: &other},
	}, nil
}

type resourceProxies struct {
	ProxyRepository
	entries []Proxy
	err     error
}

func (r *resourceProxies) ListActive(context.Context) ([]Proxy, error) { return r.entries, r.err }
func (r *resourceProxies) GetByID(_ context.Context, id int64) (*Proxy, error) {
	for i := range r.entries {
		if r.entries[i].ID == id {
			return &r.entries[i], r.err
		}
	}
	return nil, r.err
}
func (r *resourceProxies) ListByGroupID(_ context.Context, groupID int64) ([]Proxy, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := make([]Proxy, 0)
	for i := range r.entries {
		if r.entries[i].GroupID != nil && *r.entries[i].GroupID == groupID {
			out = append(out, r.entries[i])
		}
	}
	return out, nil
}
func TestPluginResourceDirectoryCatalogAndResolution(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	proxies := &resourceProxies{entries: []Proxy{
		{ID: 3, Name: "Front", Protocol: "http", Host: "front.example", Port: 8080, Username: "private-user", Password: "private-password", Status: StatusActive},
		{ID: 4, Status: StatusActive, ExpiresAt: &past}, {ID: 5, Status: "inactive"},
		{ID: 9, Name: "Other", Protocol: "http", Host: "other.example", Port: 9, Username: "other-user", Password: "other-secret", Status: StatusActive},
	}}
	base := &fakeAccountDirectory{infos: []PluginAccountInfo{{ID: 7, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth}}}
	d := NewPluginResourceDirectory(base, &resourceAccounts{}, proxies)
	scope := newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth})
	s := newPluginHostServiceServer("test.plugin", nil, d, scope)
	out, err := s.ListResources(context.Background(), &pluginv1.ListResourcesRequest{})
	require.NoError(t, err)
	require.Len(t, out.Accounts, 1)
	require.Equal(t, "Mail Pro", out.Accounts[0].Name)
	require.Len(t, out.Proxies, 1)
	b, _ := json.Marshal(out)
	require.False(t, strings.Contains(string(b), "private-"))
	for _, id := range []int64{3, 4, 5, 9, 999} {
		res, err := s.ResolveProxy(context.Background(), &pluginv1.ResolveProxyRequest{ProxyId: id})
		require.NoError(t, err)
		require.Equal(t, id == 3, res.Found)
		if id != 3 {
			require.Empty(t, res.ProxyUrl)
		}
	}
	require.NotContains(t, string(b), "other-secret")
	proxies.entries[0].Password = "updated"
	res, err := s.ResolveProxy(context.Background(), &pluginv1.ResolveProxyRequest{ProxyId: 3})
	require.NoError(t, err)
	require.Contains(t, res.ProxyUrl, "updated")
	proxies.err = errors.New("private-password")
	_, err = s.ResolveProxy(context.Background(), &pluginv1.ResolveProxyRequest{ProxyId: 3})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-password")
}
func TestPluginResourceDirectoryScopeBindsProxyAndGroup(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	groupID := int64(20)
	otherGroup := int64(21)
	bound := int64(3)
	hydratedID := int64(40)
	hiddenProxy := int64(30)
	proxies := &resourceProxies{entries: []Proxy{
		{ID: 3, Name: "Bound", Protocol: "http", Host: "bound.example", Port: 8080, Username: "bound-user", Password: "bound-secret", Status: StatusActive},
		{ID: 21, Name: "Member", Protocol: "socks5", Host: "member.example", Port: 1080, Username: "member-user", Password: "member-secret", Status: StatusActive, GroupID: &groupID},
		{ID: 22, Name: "ExpiredMember", Status: StatusActive, ExpiresAt: &past, GroupID: &groupID, Password: "expired-secret"},
		{ID: 23, Name: "OtherGroup", Protocol: "http", Host: "foreign.example", Port: 1, Password: "foreign-secret", Status: StatusActive, GroupID: &otherGroup},
		{ID: 30, Name: "HiddenProxy", Protocol: "http", Host: "hidden.example", Port: 9, Username: "hidden-user", Password: "hidden-secret", Status: StatusActive},
		{ID: 31, Name: "Loose", Protocol: "http", Host: "loose.example", Port: 9, Password: "loose-secret", Status: StatusActive},
		{ID: 40, Name: "Hydrated", Protocol: "http", Host: "hydrated.example", Port: 8081, Username: "hydrated-user", Password: "hydrated-secret", Status: StatusActive},
	}}
	accounts := &resourceAccounts{rows: []Account{
		{ID: 7, Name: "Mail Pro", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, ProxyID: &bound, ProxyGroupID: &groupID, Proxy: &Proxy{ID: hydratedID, Host: "hydrated.example", Username: "hydrated-user", Password: "hydrated-secret"}},
		{ID: 8, Name: "Hidden", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, ProxyID: &hiddenProxy, ProxyGroupID: &otherGroup},
	}}
	base := &fakeAccountDirectory{infos: []PluginAccountInfo{{ID: 7, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth}}}
	d := NewPluginResourceDirectory(base, accounts, proxies)
	scope := newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth})
	s := newPluginHostServiceServer("test.plugin", nil, d, scope)

	out, err := s.ListResources(context.Background(), &pluginv1.ListResourcesRequest{})
	require.NoError(t, err)
	require.Len(t, out.Accounts, 1)
	require.Equal(t, int64(7), out.Accounts[0].Id)
	gotIDs := make([]int64, 0, len(out.Proxies))
	for _, p := range out.Proxies {
		gotIDs = append(gotIDs, p.Id)
	}
	require.Equal(t, []int64{3, 21, 40}, gotIDs)
	catalog, err := json.Marshal(out)
	require.NoError(t, err)
	for _, secret := range []string{"bound-secret", "member-secret", "hydrated-secret", "hidden-secret", "loose-secret", "foreign-secret", "expired-secret"} {
		require.NotContains(t, string(catalog), secret)
	}

	for _, id := range []int64{3, 21, 40} {
		res, err := s.ResolveProxy(context.Background(), &pluginv1.ResolveProxyRequest{ProxyId: id})
		require.NoError(t, err)
		require.True(t, res.Found)
		require.NotEmpty(t, res.ProxyUrl)
	}
	member, err := s.ResolveProxy(context.Background(), &pluginv1.ResolveProxyRequest{ProxyId: 21})
	require.NoError(t, err)
	require.Contains(t, member.ProxyUrl, "member-secret")
	for _, id := range []int64{22, 23, 30, 31, 999} {
		res, err := s.ResolveProxy(context.Background(), &pluginv1.ResolveProxyRequest{ProxyId: id})
		require.NoError(t, err)
		require.False(t, res.Found, "proxy %d", id)
		require.Empty(t, res.ProxyUrl)
	}
}

type switchingAccountRepo struct {
	AccountRepository
	calls int
}

func (r *switchingAccountRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	r.calls++
	proxyID := int64(3)
	kind := AccountTypeOAuth
	if r.calls > 1 {
		kind = AccountTypeAPIKey
		proxyID = 9
	}
	return []Account{{
		ID: 7, Platform: PlatformOpenAI, Type: kind, Status: StatusActive, ProxyID: &proxyID,
	}}, nil
}

func TestPluginResourceDirectoryRejectsAccountTypeSwitch(t *testing.T) {
	accounts := &switchingAccountRepo{}
	proxies := &resourceProxies{entries: []Proxy{
		{ID: 3, Protocol: "http", Host: "oauth.example", Port: 8080, Username: "oauth-user", Password: "oauth-secret", Status: StatusActive},
		{ID: 9, Protocol: "http", Host: "apikey.example", Port: 8080, Username: "other-user", Password: "other-secret", Status: StatusActive},
	}}
	d := NewPluginResourceDirectory(&OpenAIGatewayService{accountRepo: accounts}, accounts, proxies)
	scope := newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth})
	s := newPluginHostServiceServer("test.plugin", nil, d, scope)

	res, err := s.ResolveProxy(context.Background(), &pluginv1.ResolveProxyRequest{ProxyId: 9})
	require.NoError(t, err)
	require.False(t, res.Found)
	require.Empty(t, res.ProxyUrl)
	require.NotContains(t, res.ProxyUrl, "other-secret")
}

func TestPluginResourceDirectoryKeepsPausedActiveAccount(t *testing.T) {
	future := time.Now().Add(time.Hour)
	proxyID := int64(3)
	accounts := &resourceAccounts{rows: []Account{{
		ID: 7, Name: "Paused", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		TempUnschedulableUntil: &future, ProxyID: &proxyID,
	}}}
	proxies := &resourceProxies{entries: []Proxy{{ID: 3, Protocol: "http", Host: "paused.example", Port: 8080, Status: StatusActive}}}
	base := &fakeAccountDirectory{infos: []PluginAccountInfo{{ID: 7, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth}}}
	d := NewPluginResourceDirectory(base, accounts, proxies)
	scope := newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth})
	s := newPluginHostServiceServer("test.plugin", nil, d, scope)
	out, err := s.ListResources(context.Background(), &pluginv1.ListResourcesRequest{})
	require.NoError(t, err)
	require.Len(t, out.Accounts, 1)
	require.Equal(t, "Paused", out.Accounts[0].Name)
	require.Len(t, out.Proxies, 1)
}

func TestPluginResourceDirectorySkipsMissingProxyBinding(t *testing.T) {
	valid, missing := int64(3), int64(999)
	accounts := &resourceAccounts{rows: []Account{
		{ID: 7, Name: "Valid", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, ProxyID: &valid},
		{ID: 8, Name: "Missing", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, ProxyID: &missing},
	}}
	proxies := &notFoundProxyRepo{resourceProxies: resourceProxies{entries: []Proxy{
		{ID: 3, Name: "Valid", Protocol: "http", Host: "valid.example", Port: 8080, Status: StatusActive},
	}}}
	base := &fakeAccountDirectory{infos: []PluginAccountInfo{
		{ID: 7, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth},
		{ID: 8, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth},
	}}
	d := NewPluginResourceDirectory(base, accounts, proxies)
	scope := newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth})
	s := newPluginHostServiceServer("test.plugin", nil, d, scope)

	out, err := s.ListResources(context.Background(), &pluginv1.ListResourcesRequest{})
	require.NoError(t, err)
	require.Len(t, out.Proxies, 1)
	require.Equal(t, int64(3), out.Proxies[0].Id)
	res, err := s.ResolveProxy(context.Background(), &pluginv1.ResolveProxyRequest{ProxyId: 3})
	require.NoError(t, err)
	require.True(t, res.Found)
	require.Contains(t, res.ProxyUrl, "valid.example")
	missingRes, err := s.ResolveProxy(context.Background(), &pluginv1.ResolveProxyRequest{ProxyId: missing})
	require.NoError(t, err)
	require.False(t, missingRes.Found)
}

func TestPluginResourceDirectoryPropagatesProxyRepositoryError(t *testing.T) {
	proxyID := int64(3)
	accounts := &resourceAccounts{rows: []Account{{
		ID: 7, Name: "Valid", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, ProxyID: &proxyID,
	}}}
	proxies := &resourceProxies{
		entries: []Proxy{{ID: 3, Protocol: "http", Host: "valid.example", Port: 8080, Status: StatusActive}},
		err:     errors.New("database unavailable"),
	}
	base := &fakeAccountDirectory{infos: []PluginAccountInfo{{ID: 7, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth}}}
	d := NewPluginResourceDirectory(base, accounts, proxies)
	scope := newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth})
	s := newPluginHostServiceServer("test.plugin", nil, d, scope)
	_, err := s.ListResources(context.Background(), &pluginv1.ListResourcesRequest{})
	require.Error(t, err)
	_, err = s.ResolveProxy(context.Background(), &pluginv1.ResolveProxyRequest{ProxyId: 3})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "database unavailable")
}

type notFoundProxyRepo struct {
	resourceProxies
}

func (r *notFoundProxyRepo) GetByID(ctx context.Context, id int64) (*Proxy, error) {
	p, err := r.resourceProxies.GetByID(ctx, id)
	if p == nil && err == nil {
		return nil, ErrProxyNotFound
	}
	return p, err
}

func TestPluginResourceDirectoryCapabilityGateAndLegacy(t *testing.T) {
	for _, tc := range []struct {
		d    PluginAccountDirectory
		code codes.Code
	}{{nil, codes.PermissionDenied}, {&fakeAccountDirectory{}, codes.Unimplemented}} {
		s := newPluginHostServiceServer("test.plugin", nil, tc.d, PluginAccountScope{})
		_, err := s.ListResources(context.Background(), &pluginv1.ListResourcesRequest{})
		require.Equal(t, tc.code, status.Code(err))
		_, err = s.ResolveProxy(context.Background(), &pluginv1.ResolveProxyRequest{ProxyId: 3})
		require.Equal(t, tc.code, status.Code(err))
	}
}
