package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type antigravityRefreshRegressionRepo struct {
	AccountRepository
	stored    *Account
	updates   int
	tempUntil time.Time
}

func (r *antigravityRefreshRegressionRepo) GetByID(context.Context, int64) (*Account, error) {
	clone := *r.stored
	clone.Credentials = shallowCopyMap(r.stored.Credentials)
	return &clone, nil
}

func (r *antigravityRefreshRegressionRepo) UpdateCredentials(_ context.Context, _ int64, credentials map[string]any) error {
	r.updates++
	r.stored.Credentials = shallowCopyMap(credentials)
	return nil
}

func (r *antigravityRefreshRegressionRepo) SetTempUnschedulable(_ context.Context, _ int64, until time.Time, _ string) error {
	r.tempUntil = until
	return nil
}

type antigravityRefreshRoundTripper func(*http.Request) (*http.Response, error)

func (f antigravityRefreshRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func antigravityRefreshTestHTTP(t *testing.T, fn antigravityRefreshRoundTripper) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = fn
	t.Cleanup(func() { http.DefaultTransport = old })
}

func antigravityRefreshTestAccount() *Account {
	return &Account{ID: 42, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{
			"access_token": "old-access", "refresh_token": "test-refresh",
			"expires_at": strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10),
		}}
}

func antigravityRefreshTestProvider(account *Account) (*AntigravityTokenProvider, *antigravityRefreshRegressionRepo) {
	stored := *account
	stored.Credentials = shallowCopyMap(account.Credentials)
	repo := &antigravityRefreshRegressionRepo{stored: &stored}
	oauth := &AntigravityOAuthService{}
	provider := NewAntigravityTokenProvider(repo, nil, oauth)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), NewAntigravityTokenRefresher(oauth))
	return provider, repo
}

func antigravityRefreshHTTPResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func TestAntigravityRefreshPersistsTokenWhenMetadataTimesOut(t *testing.T) {
	for _, timeout := range []time.Duration{100 * time.Millisecond, 2500 * time.Millisecond} {
		t.Run(timeout.String(), func(t *testing.T) {
			tokenCalls, metadataCalls := 0, 0
			antigravityRefreshTestHTTP(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "oauth2.googleapis.com" {
					tokenCalls++
					return antigravityRefreshHTTPResponse(req, 200, `{"access_token":"fresh-access","expires_in":3600,"token_type":"Bearer"}`), nil
				}
				metadataCalls++
				<-req.Context().Done()
				return nil, req.Context().Err()
			})
			account := antigravityRefreshTestAccount()
			account.Credentials["project_id"] = "existing-project"
			account.Credentials["plan_type"] = "pro"
			provider, repo := antigravityRefreshTestProvider(account)
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			start := time.Now()
			token, err := provider.GetAccessToken(ctx, account)
			require.NoError(t, err)
			require.Equal(t, "fresh-access", token)
			require.NoError(t, ctx.Err(), "metadata lookup must leave the refresh/persistence context alive")
			require.Less(t, time.Since(start), timeout)
			require.Equal(t, 1, tokenCalls)
			require.Equal(t, 1, repo.updates)
			require.True(t, repo.tempUntil.IsZero(), "optional metadata failure must not quarantine a refreshed account")
			require.Equal(t, "fresh-access", repo.stored.GetCredential("access_token"))
			require.Equal(t, "existing-project", account.GetCredential("project_id"))
			require.Equal(t, "pro", account.GetCredential("plan_type"))
			if timeout <= antigravityRefreshPersistBudget {
				require.Zero(t, metadataCalls, "skip optional work when the persistence budget is all that remains")
			} else {
				require.Positive(t, metadataCalls)
			}
		})
	}
}

func TestAntigravityRefreshedProjectVisibleToCurrentRequest(t *testing.T) {
	antigravityRefreshTestHTTP(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "oauth2.googleapis.com" {
			return antigravityRefreshHTTPResponse(req, 200, `{"access_token":"fresh-access","expires_in":3600}`), nil
		}
		return antigravityRefreshHTTPResponse(req, 200, `{"cloudaicompanionProject":"discovered-project","paidTier":{"id":"g1-pro-tier"}}`), nil
	})
	account := antigravityRefreshTestAccount()
	provider, repo := antigravityRefreshTestProvider(account)
	token, err := provider.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "fresh-access", token)
	project, err := resolveAntigravityProjectID(account)
	require.NoError(t, err)
	require.Equal(t, "discovered-project", project)
	require.Equal(t, project, repo.stored.GetCredential("project_id"))
	require.NotEmpty(t, account.GetCredential("plan_type"))
	require.Positive(t, account.GetCredentialAsInt64("_token_version"))
	require.Equal(t, repo.stored.GetCredentialAsInt64("_token_version"), account.GetCredentialAsInt64("_token_version"))
	account.Credentials["project_id"] = "request-local-change"
	require.Equal(t, project, repo.stored.GetCredential("project_id"), "publishing credentials must not alias the repository snapshot")
}

func TestAntigravityOAuthRetriesRespectCancellation(t *testing.T) {
	for _, method := range []string{"refresh", "metadata"} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			antigravityRefreshTestHTTP(t, func(req *http.Request) (*http.Response, error) {
				if req.Context().Err() != nil {
					return nil, req.Context().Err()
				}
				cancel()
				return antigravityRefreshHTTPResponse(req, 503, `{"error":{"message":"temporarily unavailable"}}`), nil
			})
			start := time.Now()
			svc := &AntigravityOAuthService{}
			var err error
			if method == "refresh" {
				_, err = svc.RefreshToken(ctx, "test-refresh", "")
			} else {
				_, err = svc.loadProjectIDWithRetry(ctx, "test-access", "", 3)
			}
			require.True(t, errors.Is(err, context.Canceled), "%v", err)
			require.Less(t, time.Since(start), 500*time.Millisecond)
		})
	}
}
