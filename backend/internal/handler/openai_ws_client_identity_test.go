package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Only the final network destination is replaced for OAuth's fixed endpoint.
// Admission, request construction, identity projection, WS framing and the real
// HTTP transport still run. The adapter fails closed for every other host.
type issue600LocalTransport struct {
	target    *url.URL
	transport http.RoundTripper
}

func (u issue600LocalTransport) local(req *http.Request) (*http.Request, error) {
	if req.URL.Host != "chatgpt.com" && req.URL.Host != u.target.Host {
		return nil, fmt.Errorf("unexpected fixture destination %s", req.URL.Host)
	}
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = u.target.Scheme
	cloned.URL.Host = u.target.Host
	cloned.Host = u.target.Host
	return cloned, nil
}
func (u issue600LocalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	local, err := u.local(req)
	if err != nil {
		return nil, err
	}
	return u.transport.RoundTrip(local)
}

type issue600LocalHTTPUpstream struct {
	service.HTTPUpstream
	route issue600LocalTransport
}

func (u issue600LocalHTTPUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	local, err := u.route.local(req)
	if err != nil {
		return nil, err
	}
	return u.HTTPUpstream.Do(local, proxy, id, concurrency)
}
func (u issue600LocalHTTPUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

type issue600Upstream struct {
	mu      sync.Mutex
	frames  [][]byte
	headers []http.Header
}

func (u *issue600Upstream) record(r *http.Request, body []byte) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.frames = append(u.frames, append([]byte(nil), body...))
	u.headers = append(u.headers, r.Header.Clone())
	return len(u.frames)
}
func (u *issue600Upstream) snapshot() ([][]byte, []http.Header) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]byte(nil), u.frames...), append([]http.Header(nil), u.headers...)
}
func issue600Thread(body []byte) string {
	if v := gjson.GetBytes(body, "client_metadata.thread_id").String(); v != "" {
		return v
	}
	return gjson.GetBytes(body, "response.client_metadata.thread_id").String()
}
func newIssue600NetworkHarness(t *testing.T, mode, namespace string, strict bool, headers http.Header) (*openAIWSPassthroughHandlerHarness, *issue600Upstream) {
	t.Helper()
	observed := &issue600Upstream{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		completed := func(n int) []byte {
			return []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_issue600_%d","model":"gpt-5.1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`, n))
		}
		if mode == service.OpenAIWSIngressModeHTTPBridge {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			n := observed.record(r, body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", completed(n))
			return
		}
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			_, body, err := conn.Read(ctx)
			cancel()
			if err != nil {
				return
			}
			n := observed.record(r, body)
			ctx, cancel = context.WithTimeout(r.Context(), 5*time.Second)
			err = conn.Write(ctx, coderws.MessageText, completed(n))
			cancel()
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	route := issue600LocalTransport{target: target, transport: transport}
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: route}
	t.Cleanup(func() { http.DefaultClient = oldClient })
	h := newOpenAIWSPassthroughHandlerHarnessWithOptions(t, upstream.URL, headers, map[string]string{service.SettingKeyCyberSessionIdentityStrictEnabled: strconv.FormatBool(strict)}, func(a *service.Account, cfg *config.Config, up *service.HTTPUpstream) {
		a.Extra["codex_fingerprint_mode"] = "off"
		a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
		if namespace != "none" {
			a.Type = service.AccountTypeOAuth
			a.Credentials["access_token"] = "fixture-access-token"
			a.Credentials["chatgpt_account_id"] = "fixture-upstream-account-" + namespace
			a.Extra["openai_oauth_responses_websockets_v2_enabled"] = true
			a.Extra["openai_oauth_responses_websockets_v2_mode"] = mode
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			if strings.HasPrefix(namespace, "connection") {
				a.AuthConnectionID = "fixture-authorized-" + namespace
			}
		}
		cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = mode != "legacy"
		*up = issue600LocalHTTPUpstream{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), route: route}
	})
	return h, observed
}
func issue600Send(t *testing.T, h *openAIWSPassthroughHandlerHarness, payload string, turn int, rejected bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, h.clientConn.Write(ctx, coderws.MessageText, []byte(payload)))
	for {
		_, event, err := h.clientConn.Read(ctx)
		if rejected {
			if err != nil {
				require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
				return
			}
			require.Equal(t, "error", gjson.GetBytes(event, "type").String(), "rejected turn must have no response generation: %s", event)
			require.Contains(t, []string{"cyber_session_identity_conflict", "cyber_session_identity_required"}, gjson.GetBytes(event, "error.code").String())
			_, _, err = h.clientConn.Read(ctx)
			require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
			return
		}
		require.NoError(t, err, "stable original client identity must admit turn %d", turn)
		require.NotEqual(t, "error", gjson.GetBytes(event, "type").String(), "stable original client identity must admit turn %d: %s", turn, event)
		if gjson.GetBytes(event, "type").String() == "response.completed" {
			return
		}
	}
}
func issue600Finish(t *testing.T, h *openAIWSPassthroughHandlerHarness, headers http.Header) {
	t.Helper()
	require.NoError(t, h.clientConn.CloseNow())
	select {
	case <-h.handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not release connection")
	}
	select {
	case final := <-h.finalHeaders:
		for key, values := range headers {
			require.Equal(t, values, final.Values(key), "original upgrade header %s must stay untouched", key)
		}
		require.Equal(t, headers.Get("thread-id"), final.Get("thread-id"))
		require.Equal(t, headers.Get("session_id"), final.Get("session_id"))
	default:
		t.Fatal("original upgrade headers were not captured")
	}
}

// Real handler -> production forwarder -> synthetic WS/HTTP upstream. No call to
// an extracted identity resolver or an admission hook stands in for the network.
func TestIssue600ClientIdentityNetwork(t *testing.T) {
	stable := `{"type":"response.create","model":"gpt-5.1","client_metadata":{"thread_id":"fixture-client-thread"},"input":"fixture"}`
	omitted := `{"type":"response.create","model":"gpt-5.1","input":"fixture-followup"}`
	changed := strings.ReplaceAll(stable, "fixture-client-thread", "fixture-different-thread")
	invalid := strings.ReplaceAll(stable, "fixture-client-thread", `fixture\ninvalid`)
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough, "legacy"} {
		t.Run(mode, func(t *testing.T) {
			for _, strict := range []bool{false, true} {
				t.Run("strict="+strconv.FormatBool(strict), func(t *testing.T) {
					for _, namespace := range []string{"none", "oauth", "connection"} {
						t.Run(namespace, func(t *testing.T) {
							for _, tc := range []struct {
								name          string
								headers       http.Header
								first, second string
								reject        bool
							}{
								{"stable_header_body", http.Header{"Thread-Id": []string{"fixture-client-thread"}}, stable, stable, false},
								{"body_only", nil, stable, stable, false},
								{"omitted_identity", nil, stable, omitted, false},
								{"static_session_inheritance", http.Header{"Session_id": []string{"fixture-static-session"}}, stable, omitted, false},
								{"genuine_header_body_conflict", http.Header{"Thread-Id": []string{"fixture-client-thread"}}, stable, changed, true},
								{"true_body_identity_switch", nil, stable, changed, true},
								{"invalid_followup", nil, stable, invalid, true},
							} {
								t.Run(tc.name, func(t *testing.T) {
									h, up := newIssue600NetworkHarness(t, mode, namespace, strict, tc.headers)
									issue600Send(t, h, tc.first, 1, false)
									issue600Send(t, h, tc.second, 2, tc.reject)
									issue600Finish(t, h, tc.headers)
									frames, outHeaders := up.snapshot()
									want := 2
									if tc.reject {
										want = 1
									}
									require.Len(t, frames, want, "rejected turn must cause zero extra upstream generations")
									projected := issue600Thread(frames[0])
									require.NotEmpty(t, projected)
									if namespace != "none" {
										require.NotEqual(t, "fixture-client-thread", projected, "original identity must not leak upstream")
									}
									for i, frame := range frames {
										if thread := issue600Thread(frame); thread != "" {
											require.Equal(t, projected, thread)
										}
										if tc.headers.Get("thread-id") != "" {
											require.Equal(t, projected, outHeaders[i].Get("thread-id"), "outbound header/body projection must agree")
										}
										if namespace != "none" {
											require.NotContains(t, string(frame), "fixture-client-thread")
											require.NotContains(t, fmt.Sprint(outHeaders[i]), "fixture-client-thread")
											require.NotContains(t, fmt.Sprint(outHeaders[i]), "fixture-static-session")
										}
									}
								})
							}
						})
					}
				})
			}
		})
	}
}

func TestIssue600NamespaceAccountBoundaries(t *testing.T) {
	payload := `{"type":"response.create","model":"gpt-5.1","client_metadata":{"thread_id":"fixture-client-thread"},"input":"fixture"}`
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough, "legacy"} {
		t.Run(mode, func(t *testing.T) {
			var identities []string
			for _, namespace := range []string{"oauth-a", "oauth-b", "connection-a", "connection-b"} {
				// Each selection uses an independent connection. A client cannot move its
				// namespace into another selected credential, even with identical input.
				t.Run(namespace, func(t *testing.T) {
					headers := http.Header{"Thread-Id": []string{"fixture-client-thread"}}
					h, up := newIssue600NetworkHarness(t, mode, namespace, true, headers)
					issue600Send(t, h, payload, 1, false)
					issue600Send(t, h, payload, 2, false)
					issue600Finish(t, h, headers)
					frames, outHeaders := up.snapshot()
					require.Len(t, frames, 2)
					identity := issue600Thread(frames[0])
					require.NotEqual(t, "fixture-client-thread", identity)
					require.Equal(t, identity, issue600Thread(frames[1]))
					require.Equal(t, identity, outHeaders[1].Get("thread-id"))
					require.NotContains(t, identities, identity, "different account/authorized connection must use distinct outbound namespace")
					identities = append(identities, identity)
				})
			}
		})
	}
}

func TestIssue600WSTurnRejectionDiagnostic(t *testing.T) {
	// Decode the new metadata flag so this exact test file also compiles in the
	// disposable baseline RED checkout, where the old struct ignores that flag.
	var meta cyberPolicyOpsErrorMeta
	require.NoError(t, json.Unmarshal([]byte(`{"WSTurnAdmission":true,"IdentityStatus":"conflict","IdentityKind":"thread","IdentitySource":"header_body"}`), &meta))
	entry := buildCyberSessionIdentityRejectedOpsEntry(meta)
	require.Contains(t, entry.ErrorMessage, "websocket turn rejected during session identity admission")
	require.NotContains(t, entry.ErrorMessage, "before account selection")
	require.Contains(t, entry.ErrorMessage, "session_identity_status=conflict")
}
