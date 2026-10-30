package service

import (
	"context"
	"math"
	"sort"
)

// Uses existing passive transport evidence, scoped before model routing. It
// changes only ordering among otherwise eligible same-priority accounts.
// No evidence/cold start is neutral; all evidence expires after ten minutes.
func (s *OpenAIGatewayService) defaultFeedbackPenalty(ctx context.Context, group *int64, model string, accountID int64) float64 {
	if s == nil {
		return 0
	}
	s.initOpenAIAccountScheduler()
	identity := priorityObservationFromContext(ctx, model)
	if identity.endpoint == "" {
		identity.endpoint = "/v1/responses"
	}
	key := priorityAttemptKey{accountID: accountID, model: identity.model, endpoint: identity.endpoint, groupID: identity.groupID, hasGroup: identity.hasGroup}
	key.effort, _ = observationEffort(ctx)
	if !identity.hasGroup && group != nil {
		key.groupID, key.hasGroup = *group, true
	}
	e := s.openaiAccountStats.attempts.snapshot(key, s.openaiAccountStats.currentTime(), 10, 5)
	penalty := 0.0
	if e.Successes+e.Failures >= 5 {
		penalty = 25 * e.SmoothedRate
	}
	if e.LatencySamples >= 5 {
		mean := e.LatencyTotalMS / float64(e.LatencySamples)
		penalty += 10 * math.Max(0, math.Min(1, mean/15000-1))
	}
	return math.Min(35, penalty)
}

func (s *OpenAIGatewayService) defaultFeedbackPenalties(ctx context.Context, group *int64, model string, accounts []*Account) map[int64]float64 {
	out := make(map[int64]float64, len(accounts))
	for _, a := range accounts {
		out[a.ID] = s.defaultFeedbackPenalty(ctx, group, model, a.ID)
	}
	return out
}

func orderDefaultFeedback(accounts []*Account, penalties map[int64]float64) {
	sort.SliceStable(accounts, func(i, j int) bool {
		if accounts[i].Priority != accounts[j].Priority {
			return accounts[i].Priority < accounts[j].Priority
		}
		return penalties[accounts[i].ID] < penalties[accounts[j].ID]
	})
}

func (s *defaultOpenAIAccountScheduler) shouldEscapeStickyForRequest(req OpenAIAccountScheduleRequest, id int64, cfg openAIStickyEscapeConfig) (string, float64, float64, bool) {
	if !req.ObservationEffortScoped {
		return s.shouldEscapeStickyAccount(id, cfg)
	}
	if !cfg.enabled || s.stats == nil {
		return "", 0, 0, false
	}
	e := s.stats.attempts.snapshot(priorityRequestAttemptKey(req, id), s.stats.currentTime(), 10, 5)
	if e.LatencySamples >= 5 {
		ttft := e.LatencyTotalMS / float64(e.LatencySamples)
		if ttft > cfg.ttftMs {
			return "ttft", e.SmoothedRate, ttft, true
		}
	}
	if e.Successes+e.Failures >= 5 && e.SmoothedRate > cfg.errorRate {
		return "error_rate", e.SmoothedRate, 0, true
	}
	return "", 0, 0, false
}
