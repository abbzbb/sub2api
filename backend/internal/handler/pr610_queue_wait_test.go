package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type pr610Barrier struct {
	phase   string
	release chan struct{}
}

// The clock is request-scoped. Barriers stall the actual slot-cache and pool
// acquisition calls in authenticated handler -> production ingress -> network.
// Busy user/account slots still reject according to the existing WS policy;
// these barriers model a delayed acquisition, not a new capacity-wait policy.
type pr610TimingScenario struct {
	t             *testing.T
	mode          string
	realtime      bool
	base          time.Time
	offset        atomic.Int64
	turn          atomic.Int32
	poolHolder    bool
	holderHandled atomic.Bool
	holderArrived chan struct{}
	holderRelease chan struct{}
	holderDone    chan struct{}
	gateway       *service.OpenAIGatewayService
	retry         bool
	longFirst     bool
	attempts      atomic.Int32
	mu            sync.Mutex
	gated         map[string]bool
	barriers      chan pr610Barrier
	deltaRead     chan struct{}
	cache         service.ConcurrencyCache
}

func newPR610Scenario(t *testing.T, mode string) *pr610TimingScenario {
	t.Helper()
	s := &pr610TimingScenario{t: t, mode: mode, base: time.Now(), barriers: make(chan pr610Barrier), deltaRead: make(chan struct{}), gated: map[string]bool{}}
	s.cache = &pr610SlotCache{ConcurrencyCache: testutil.NewRedisConcurrencyCache(t), s: s}
	return s
}
func (s *pr610TimingScenario) now() time.Time {
	if s.realtime {
		return time.Now()
	}
	return s.base.Add(time.Duration(s.offset.Load()))
}
func (s *pr610TimingScenario) advance(d time.Duration) {
	if s.realtime {
		time.Sleep(d / 100)
		return
	}
	s.offset.Add(int64(d))
}

type pr610HolderKey struct{}

func (s *pr610TimingScenario) middleware(c *gin.Context) {
	if c.GetHeader("X-PR610-Pool-Holder") == "true" {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), pr610HolderKey{}, true))
	}
	c.Request = c.Request.WithContext(requesttiming.WithClock(c.Request.Context(), s.now))
	c.Next()
}
func (s *pr610TimingScenario) configure(a *service.Account, cfg *config.Config) {
	require.Equal(s.t, s.mode, a.ResolveOpenAIResponsesWebSocketV2Mode(cfg.Gateway.OpenAIWS.IngressModeDefault), "fixture must resolve the requested effective mode")
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	if s.poolHolder {
		a.Concurrency = 2
	}
}
func (s *pr610TimingScenario) block(phase string) {
	s.mu.Lock()
	if s.gated[phase] {
		s.mu.Unlock()
		return
	}
	s.gated[phase] = true
	s.mu.Unlock()
	b := pr610Barrier{phase: phase, release: make(chan struct{})}
	select {
	case s.barriers <- b:
	case <-time.After(3 * time.Second):
		s.t.Error("barrier was not consumed: " + phase)
		return
	}
	select {
	case <-b.release:
	case <-time.After(3 * time.Second):
		s.t.Error("barrier was not released: " + phase)
	}
}
func (s *pr610TimingScenario) prepareTurn() {
	turn := s.turn.Add(1)
	s.mu.Lock()
	s.gated = map[string]bool{}
	s.mu.Unlock()
	phases := []string{"user", "account"}
	if turn == 1 && s.mode == service.OpenAIWSIngressModeCtxPool && !s.poolHolder {
		phases = append(phases, "pool")
	}
	go func() {
		for _, phase := range phases {
			select {
			case b := <-s.barriers:
				if b.phase != phase {
					s.t.Errorf("barrier order: got %s want %s", b.phase, phase)
				}
				d := 10 * time.Second
				if phase == "account" {
					d = 20 * time.Second
				}
				s.advance(d)
				close(b.release)
			case <-time.After(3 * time.Second):
				s.t.Error("production path did not reach barrier: " + phase)
				return
			}
		}
		if s.poolHolder {
			require.Eventually(s.t, func() bool { return s.gateway.SnapshotOpenAIWSPoolMetrics().AcquireQueueWaitTotal > 0 }, 3*time.Second, time.Millisecond, "target must enter real busy lease queue")
			s.advance(10 * time.Second)
			close(s.holderRelease)
		}
	}()
}
func (s *pr610TimingScenario) poolHandshake() {
	if s.mode == service.OpenAIWSIngressModeCtxPool && !s.poolHolder {
		s.block("pool")
	}
}
func (s *pr610TimingScenario) terminal(payload []byte, turn int) string {
	return fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_usage_e2e_%d","model":%q,"usage":{"input_tokens":2,"output_tokens":1}}}`, turn, gjson.GetBytes(payload, "model").String())
}
func (s *pr610TimingScenario) afterDelta() {
	select {
	case <-s.deltaRead:
	case <-time.After(3 * time.Second):
		s.t.Error("synthetic first token did not reach downstream")
	}
	if s.longFirst && s.turn.Load() == 1 {
		s.advance(80 * time.Second)
	}
}
func (s *pr610TimingScenario) writeWS(c *coderws.Conn, turn int, payload []byte) {
	s.advance(2 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(s.t, c.Write(ctx, coderws.MessageText, []byte(`{"type":"response.output_text.delta","response_id":"resp_usage_e2e_`+fmt.Sprint(turn)+`","delta":"x"}`)))
	s.afterDelta()
	require.NoError(s.t, c.Write(ctx, coderws.MessageText, []byte(s.terminal(payload, turn))))
}
func (s *pr610TimingScenario) serveHTTP(w http.ResponseWriter, r *http.Request, received chan<- []byte) bool {
	if s.mode != service.OpenAIWSIngressModeHTTPBridge {
		return false
	}
	payload, err := io.ReadAll(r.Body)
	require.NoError(s.t, err)
	if s.retry && s.attempts.Add(1) == 1 {
		s.advance(7 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"invalid_parameter","param":"truncation","message":"Unsupported parameter: truncation"}}`)
		return true
	}
	received <- payload
	s.advance(2 * time.Second)
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"response_id\":\"resp_usage_e2e_%d\",\"delta\":\"x\"}\n\n", s.turn.Load())
	w.(http.Flusher).Flush()
	s.afterDelta()
	_, _ = fmt.Fprintf(w, "data: %s\n\n", s.terminal(payload, int(s.turn.Load())))
	return true
}
func (s *pr610TimingScenario) assertReleased() {
	if s.poolHolder {
		select {
		case <-s.holderDone:
		case <-time.After(3 * time.Second):
			s.t.Fatal("pool holder did not release")
		}
	}
	users, err := s.cache.GetUserConcurrency(context.Background(), 1701)
	require.NoError(s.t, err)
	require.Zero(s.t, users)
	accounts, err := s.cache.GetAccountConcurrency(context.Background(), 9901)
	require.NoError(s.t, err)
	require.Zero(s.t, accounts)
}

type pr610SlotCache struct {
	service.ConcurrencyCache
	s *pr610TimingScenario
}

func (c *pr610SlotCache) AcquireUserSlot(ctx context.Context, id int64, limit int, member string) (bool, error) {
	if ctx.Value(pr610HolderKey{}) != true {
		c.s.block("user")
	}
	return c.ConcurrencyCache.AcquireUserSlot(ctx, id, limit, member)
}
func (c *pr610SlotCache) AcquireAccountSlot(ctx context.Context, id int64, limit int, member string) (bool, error) {
	if ctx.Value(pr610HolderKey{}) != true {
		c.s.block("account")
	}
	return c.ConcurrencyCache.AcquireAccountSlot(ctx, id, limit, member)
}

type pr610AuthRepo struct {
	service.APIKeyRepository
	key *service.APIKey
}

func (r *pr610AuthRepo) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	if key != r.key.Key {
		return nil, service.ErrAPIKeyNotFound
	}
	return r.key, nil
}
func (*pr610AuthRepo) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }
func pr610Auth(key *service.APIKey, cfg *config.Config) *service.APIKeyService {
	key.User.Concurrency = 1
	key.Group = &service.Group{ID: *key.GroupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	return service.NewAPIKeyService(&pr610AuthRepo{key: key}, nil, nil, nil, nil, nil, cfg)
}

func TestPR610AuthenticatedTurnsCountWaitOnce(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			s := newPR610Scenario(t, mode)
			s.longFirst = true
			got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: mode, authSetup: pr610Auth,
				firstPayload:  `{"type":"response.create","model":"gpt-5.4","stream":true}`,
				secondPayload: `{"type":"response.create","model":"gpt-5.4","stream":true}`})
			require.Len(t, got.logs, 2)
			for i, row := range got.logs {
				q := int64(30000)
				if i == 0 && mode == service.OpenAIWSIngressModeCtxPool {
					q += 10000
				}
				require.NotNil(t, row.QueueWaitMs, "authenticated native turn must publish measured pre-forward wait")
				require.EqualValues(t, q, *row.QueueWaitMs, "accepted frame must count each delayed slot/pool acquisition exactly once")
				require.NotNil(t, row.QueueTiming, "explicit sampled timing source must survive the real usage path")
				require.Equal(t, "ws_frame_"+mode, row.QueueTiming.Source, "effective mode must match fixture")
				require.EqualValues(t, q+2000, *row.QueueTiming.ServerFirstTokenMs)
				require.EqualValues(t, 2000, *row.QueueTiming.ForwardFirstTokenMs)
				legacyFirst := int64(2000)
				if mode == service.OpenAIWSIngressModePassthrough {
					legacyFirst += q
				}
				require.EqualValues(t, legacyFirst, *row.FirstTokenMs, "legacy timer contract must not be redefined")
				tail := int64(0)
				if i == 0 {
					tail = 80000
				}
				require.EqualValues(t, q+2000+tail, row.QueueTiming.ServerDurationMs)
				require.EqualValues(t, 2000+tail, row.QueueTiming.ForwardDurationMs)
				require.EqualValues(t, legacyFirst+tail, *row.DurationMs, "legacy duration remains the TPS denominator")
			}
		})
	}
}

func TestPR610BridgeRejectedFieldRetryKeepsLegacyDenominator(t *testing.T) {
	s := newPR610Scenario(t, service.OpenAIWSIngressModeHTTPBridge)
	s.retry = true
	got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: s.mode, authSetup: pr610Auth,
		firstPayload: `{"type":"response.create","model":"gpt-5.4","stream":true,"truncation":"auto"}`})
	require.EqualValues(t, 2, s.attempts.Load(), "synthetic upstream must exercise the real rejected-field retry")
	row := got.log
	require.EqualValues(t, 30000, *row.QueueWaitMs, "failed network attempt must not move into queue")
	require.EqualValues(t, 9000, *row.FirstTokenMs, "bridge legacy first must include the rejected-field attempt")
	require.EqualValues(t, 9000, *row.DurationMs, "bridge legacy TPS denominator must include the rejected-field attempt")
	require.NotNil(t, row.QueueTiming)
	require.EqualValues(t, 39000, *row.QueueTiming.ServerFirstTokenMs)
	require.EqualValues(t, 9000, row.QueueTiming.ForwardDurationMs)
}

// This counterpart uses actual wall time, so the frozen defective source can
// consume the same fixture without depending on the newly introduced clock seam.
func TestPR610RealWaitNativeIngress(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough} {
		t.Run(mode, func(t *testing.T) {
			s := newPR610Scenario(t, mode)
			s.realtime = true
			s.longFirst = true
			got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: mode, authSetup: pr610Auth,
				firstPayload:  `{"type":"response.create","model":"gpt-5.4","stream":true}`,
				secondPayload: `{"type":"response.create","model":"gpt-5.4","stream":true}`})
			for i, row := range got.logs {
				want := 300
				if i == 0 && mode == service.OpenAIWSIngressModeCtxPool {
					want = 400
				}
				require.NotNil(t, row.QueueWaitMs, "native authenticated turn must retain a measured wait")
				require.GreaterOrEqual(t, *row.QueueWaitMs, want-10, "accepted frame must count real blocked acquisition before forward")
				require.Less(t, *row.QueueWaitMs, want+150, "each wait must be counted once, without previous-turn tail")
				require.NotNil(t, row.QueueTiming, "measured source must survive authenticated production usage path")
				require.InDelta(t, 20, *row.QueueTiming.ForwardFirstTokenMs, 100)
				require.GreaterOrEqual(t, *row.QueueTiming.ServerFirstTokenMs, int64(want+10))
				if mode == service.OpenAIWSIngressModePassthrough {
					require.InDelta(t, *row.FirstTokenMs, *row.QueueTiming.ServerFirstTokenMs, 2, "passthrough legacy already includes queue")
				}
			}
		})
	}
}
func TestPR610RealBridgeRetryDenominator(t *testing.T) {
	s := newPR610Scenario(t, service.OpenAIWSIngressModeHTTPBridge)
	s.realtime = true
	s.retry = true
	got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: s.mode, authSetup: pr610Auth,
		firstPayload: `{"type":"response.create","model":"gpt-5.4","stream":true,"truncation":"auto"}`})
	require.EqualValues(t, 2, s.attempts.Load())
	row := got.log
	require.GreaterOrEqual(t, *row.DurationMs, 85, "rejected-field network time must stay in legacy TPS denominator")
	require.Less(t, *row.QueueWaitMs, 380, "rejected-field network time must not move into queue")
}

func (s *pr610TimingScenario) startHolder(serverURL, key string, gateway *service.OpenAIGatewayService) {
	s.gateway = gateway
	s.holderArrived = make(chan struct{})
	s.holderRelease = make(chan struct{})
	s.holderDone = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(serverURL, "http")+"/v1/responses", &coderws.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key}, "X-PR610-Pool-Holder": {"true"}}})
	require.NoError(s.t, err)
	require.NoError(s.t, c.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","stream":true,"input":"pool-holder"}`)))
	go func() {
		defer close(s.holderDone)
		defer func() { _ = c.CloseNow() }()
		readCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, event, err := c.Read(readCtx)
		if err != nil {
			s.t.Error(err)
			return
		}
		if gjson.GetBytes(event, "response.id").String() != "resp_pool_holder" {
			s.t.Errorf("unexpected pool holder event %s", event)
		}
		_ = c.Close(coderws.StatusNormalClosure, "release lease")
	}()
	select {
	case <-s.holderArrived:
	case <-time.After(3 * time.Second):
		s.t.Fatal("holder did not acquire real pool lease")
	}
}
func (s *pr610TimingScenario) handleHolder(c *coderws.Conn, payload []byte) bool {
	if !s.poolHolder || gjson.GetBytes(payload, "input").String() != "pool-holder" || !s.holderHandled.CompareAndSwap(false, true) {
		return false
	}
	close(s.holderArrived)
	select {
	case <-s.holderRelease:
	case <-time.After(5 * time.Second):
		s.t.Error("target did not release pool holder")
		return true
	}
	require.NoError(s.t, c.Write(context.Background(), coderws.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_pool_holder","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}}`)))
	return true
}
func TestPR610AuthenticatedBusyPoolLeaseCountsOnce(t *testing.T) {
	s := newPR610Scenario(t, service.OpenAIWSIngressModeCtxPool)
	s.poolHolder = true
	got := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{timing: s, ingressMode: s.mode,
		authSetup: func(key *service.APIKey, cfg *config.Config) *service.APIKeyService {
			svc := pr610Auth(key, cfg)
			key.User.Concurrency = 2
			return svc
		},
		firstPayload: `{"type":"response.create","model":"gpt-5.4","stream":true,"input":"pool-holder"}`})
	require.Greater(t, s.gateway.SnapshotOpenAIWSPoolMetrics().AcquireQueueWaitTotal, int64(0), "must contend on a busy pool lease")
	require.NotNil(t, got.log.QueueTiming)
	require.EqualValues(t, 40000, *got.log.QueueWaitMs, "actual busy lease wait and both slot calls count once")
	require.EqualValues(t, 42000, *got.log.QueueTiming.ServerFirstTokenMs)
	require.EqualValues(t, 2000, *got.log.FirstTokenMs)
}
