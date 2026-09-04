package service

import (
	"log/slog"
	"sync"
	"time"
)

// Ambiguous Grok 403/404 (no model/endpoint scope, no hard-death marker, no
// billing/entitlement marker) is intentionally NOT cooled on a single hit: in
// pool mode a bare 403 is often a content-policy or transient permission blip
// and a 30m cooldown would strand healthy accounts.
//
// The other side of that trade-off is an account that is genuinely broken but
// reports it without any recognizable marker: it stays in rotation, fails on
// every request and burns one failover slot each time until an operator steps
// in. The tracker below closes that gap with a runtime-only (in-process, never
// persisted) escalating cooldown that only trips on repeated failures within a
// short window. Any successful upstream response resets the account.
const (
	// grokAmbiguousFailureWindow bounds how close together the failures must be.
	grokAmbiguousFailureWindow = 60 * time.Second
	// grokAmbiguousFailureThreshold is the number of failures inside the window
	// needed to trip a cooldown. A single ambiguous hit never changes state.
	grokAmbiguousFailureThreshold = 3
	// grokAmbiguousFailureDecay resets the escalation level once the account
	// has been quiet (no ambiguous failures, and not inside a cooldown it
	// tripped) for this long.
	grokAmbiguousFailureDecay  = 15 * time.Minute
	grokAmbiguousFailureReason = "grok repeated ambiguous 403/404"
)

// grokAmbiguousCooldownLadder is indexed by escalation level; the last entry is
// the cap. Values are deliberately far below the legacy 30m one-shot cooldown at
// the first step so a false positive costs little.
var grokAmbiguousCooldownLadder = []time.Duration{
	2 * time.Minute,
	10 * time.Minute,
	30 * time.Minute,
}

type grokAmbiguousFailureState struct {
	mu          sync.Mutex
	count       int
	windowStart time.Time
	// quietSince is the later of the last failure and the end of the last
	// cooldown this tracker tripped; decay is measured from it so a burst right
	// after a long cooldown expires still escalates instead of starting over.
	quietSince time.Time
	level      int
}

// grokAmbiguousFailureTracker keeps per-account counters. It is safe for
// concurrent use and its zero value is ready.
type grokAmbiguousFailureTracker struct {
	states sync.Map // key: int64(accountID), value: *grokAmbiguousFailureState
}

func (t *grokAmbiguousFailureTracker) state(accountID int64) *grokAmbiguousFailureState {
	if v, ok := t.states.Load(accountID); ok {
		if st, ok := v.(*grokAmbiguousFailureState); ok {
			return st
		}
	}
	actual, _ := t.states.LoadOrStore(accountID, &grokAmbiguousFailureState{})
	st, _ := actual.(*grokAmbiguousFailureState)
	return st
}

// record registers one ambiguous failure at now. When the threshold is reached
// it returns the cooldown to apply and true; the window counter is reset and the
// escalation level advanced so the next trip cools longer.
func (t *grokAmbiguousFailureTracker) record(accountID int64, now time.Time) (time.Duration, bool) {
	if t == nil {
		return 0, false
	}
	st := t.state(accountID)
	st.mu.Lock()
	defer st.mu.Unlock()

	if !st.quietSince.IsZero() && now.Sub(st.quietSince) > grokAmbiguousFailureDecay {
		st.level = 0
		st.count = 0
	}
	if st.count == 0 || now.Sub(st.windowStart) > grokAmbiguousFailureWindow {
		st.windowStart = now
		st.count = 0
	}
	st.count++
	if now.After(st.quietSince) {
		st.quietSince = now
	}
	if st.count < grokAmbiguousFailureThreshold {
		return 0, false
	}

	level := st.level
	if level >= len(grokAmbiguousCooldownLadder) {
		level = len(grokAmbiguousCooldownLadder) - 1
	}
	cooldown := grokAmbiguousCooldownLadder[level]
	if st.level < len(grokAmbiguousCooldownLadder)-1 {
		st.level++
	}
	st.count = 0
	st.quietSince = now.Add(cooldown)
	return cooldown, true
}

// reset forgets all state for the account. Called on a successful upstream
// response so a recovered account starts from a clean slate.
func (t *grokAmbiguousFailureTracker) reset(accountID int64) {
	if t == nil {
		return
	}
	t.states.Delete(accountID)
}

// noteGrokAmbiguousFailure feeds the tracker and, when it trips, installs a
// runtime-only scheduling block. It never touches durable account state, so
// nothing survives a restart and no admin action is needed to undo it.
func (s *OpenAIGatewayService) noteGrokAmbiguousFailure(account *Account, statusCode int) {
	if s == nil || account == nil {
		return
	}
	now := time.Now()
	cooldown, tripped := s.grokAmbiguousFailures.record(account.ID, now)
	if !tripped {
		return
	}
	s.BlockAccountScheduling(account, now.Add(cooldown), grokAmbiguousFailureReason)
	slog.Warn("grok_ambiguous_failure_runtime_cooldown",
		"account_id", account.ID,
		"status_code", statusCode,
		"cooldown", cooldown.String(),
		"threshold", grokAmbiguousFailureThreshold,
		"window", grokAmbiguousFailureWindow.String(),
	)
}

// noteGrokUpstreamSuccess clears any ambiguous-failure history for the account.
func (s *OpenAIGatewayService) noteGrokUpstreamSuccess(account *Account) {
	if s == nil || account == nil {
		return
	}
	s.grokAmbiguousFailures.reset(account.ID)
}
