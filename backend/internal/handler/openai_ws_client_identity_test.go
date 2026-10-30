package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// These are real network requests through ResponsesWebSocket and the production
// forwarder, never direct calls to the identity resolver or hook.
func TestIssue600ClientIdentityNetwork(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough, "legacy"} {
		t.Run(mode, func(t *testing.T) {
			for _, strict := range []bool{false, true} {
				t.Run("strict="+strconv.FormatBool(strict), func(t *testing.T) {
					var mu sync.Mutex
					var frames [][]byte
					var headers []http.Header
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						record := func(payload []byte) int {
							mu.Lock()
							defer mu.Unlock()
							frames = append(frames, append([]byte(nil), payload...))
							headers = append(headers, r.Header.Clone())
							return len(frames)
						}
						completed := func(n int) []byte {
							return []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_issue600_%d","model":"gpt-5.1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`, n))
						}
						if mode == service.OpenAIWSIngressModeHTTPBridge {
							body, err := io.ReadAll(r.Body)
							if err != nil {
								t.Error(err)
								return
							}
							n := record(body)
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "data: %s\n\n", completed(n))
							return
						}
						conn, err := coderws.Accept(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.CloseNow()
						for {
							ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
							_, body, err := conn.Read(ctx)
							cancel()
							if err != nil {
								return
							}
							n := record(body)
							ctx, cancel = context.WithTimeout(r.Context(), 5*time.Second)
							err = conn.Write(ctx, coderws.MessageText, completed(n))
							cancel()
							if err != nil {
								return
							}
						}
					}))
					defer upstream.Close()
					h := newOpenAIWSPassthroughHandlerHarnessWithOptions(t, upstream.URL, http.Header{"Thread-Id": []string{"fixture-client-thread"}}, map[string]string{service.SettingKeyCyberSessionIdentityStrictEnabled: strconv.FormatBool(strict)}, func(a *service.Account, cfg *config.Config) {
						a.AuthConnectionID = "fixture-authorized-connection"
						a.Extra["codex_fingerprint_mode"] = "off"
						a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
						cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = mode != "legacy"
					})
					for turn := 1; turn <= 2; turn++ {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						err := h.clientConn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","client_metadata":{"thread_id":"fixture-client-thread"},"input":"fixture"}`))
						require.NoError(t, err)
						for {
							_, event, err := h.clientConn.Read(ctx)
							require.NoError(t, err, "stable original client identity must admit turn %d", turn)
							require.NotEqual(t, "error", gjson.GetBytes(event, "type").String(), "stable original client identity must admit turn %d: %s", turn, event)
							if gjson.GetBytes(event, "type").String() == "response.completed" {
								break
							}
						}
						cancel()
					}
					mu.Lock()
					require.Len(t, frames, 2, "each admitted turn must generate exactly once")
					projected := gjson.GetBytes(frames[0], "client_metadata.thread_id").String()
					require.NotEmpty(t, projected)
					require.NotEqual(t, "fixture-client-thread", projected, "original identity must not leak upstream")
					for i, frame := range frames {
						require.Equal(t, projected, gjson.GetBytes(frame, "client_metadata.thread_id").String())
						require.Equal(t, projected, headers[i].Get("thread-id"), "outbound header/body projection must agree")
					}
					mu.Unlock()
					require.NoError(t, h.clientConn.CloseNow())
					select {
					case <-h.handlerDone:
					case <-time.After(5 * time.Second):
						t.Fatal("handler did not release connection")
					}
				})
			}
		})
	}
}
