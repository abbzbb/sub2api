package requesttiming

import "time"

// AccountTestMetric is a bounded observation, never an inferred model CPU time.
// Missing values use a reason code; an observed zero remains a valid value.
type AccountTestMetric struct {
	StartMS    *float64 `json:"start_ms,omitempty"`
	EndMS      *float64 `json:"end_ms,omitempty"`
	DurationMS *float64 `json:"duration_ms,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// AccountTestTiming is an explicit public allowlist. Never embed Snapshot,
// Attempt, event maps, routes, account/proxy IDs, headers or error strings here.
type AccountTestTiming struct {
	Version         int               `json:"version"`
	StartedAt       time.Time         `json:"started_at"`
	FinishedAt      *time.Time        `json:"finished_at,omitempty"`
	TotalMS         *float64          `json:"total_ms,omitempty"`
	Outcome         string            `json:"outcome"`
	Mode            string            `json:"mode"`
	Connection      AccountTestMetric `json:"connection"`
	ResponseHeaders AccountTestMetric `json:"response_headers"`
	FirstContent    AccountTestMetric `json:"first_content"`
	StreamOutput    AccountTestMetric `json:"stream_output"`
	LocalProcessing AccountTestMetric `json:"local_processing"`
}

func missingAccountMetric(reason string) AccountTestMetric { return AccountTestMetric{Reason: reason} }

func accountMetric(start, end, total float64) AccountTestMetric {
	if start < 0 || end < start || end > total {
		return missingAccountMetric("invalid_observation")
	}
	duration := end - start
	return AccountTestMetric{StartMS: &start, EndMS: &end, DurationMS: &duration}
}

func AccountTestStarted(start time.Time) AccountTestTiming {
	return AccountTestTiming{Version: 1, StartedAt: start, Outcome: "running", Mode: "unobserved",
		Connection: missingAccountMetric("not_observed"), ResponseHeaders: missingAccountMetric("not_observed"),
		FirstContent: missingAccountMetric("no_content"), StreamOutput: missingAccountMetric("not_observed"),
		LocalProcessing: missingAccountMetric("not_isolated")}
}

// AccountTestProjection copies only approved scalar measurements. Multiple
// requests/retries cannot be represented as a single network phase honestly.
func AccountTestProjection(d Snapshot) AccountTestTiming {
	out := AccountTestStarted(d.StartedAt)
	end := d.StartedAt.Add(time.Duration(d.TotalMS * float64(time.Millisecond)))
	out.FinishedAt, out.TotalMS, out.Outcome = &end, &d.TotalMS, d.Outcome
	if d.TTFTMode == "stream" || d.TTFTMode == "non_stream" {
		out.Mode = d.TTFTMode
	}
	var attempts []Attempt
	for _, a := range d.Attempts {
		if a.Kind == "egress" {
			attempts = append(attempts, a)
		}
	}
	if len(attempts) == 1 {
		a := attempts[0]
		if a.Reused != nil && *a.Reused {
			out.Connection = missingAccountMetric("connection_reused")
		} else if start, ok := a.Events["connection_start"]; ok {
			if end, ok := a.Events["connection_ready"]; ok {
				out.Connection = accountMetric(start, end, d.TotalMS)
			}
		}
		if start, ok := a.Events["request_written"]; ok {
			if end, ok := a.Events["response_headers"]; ok {
				out.ResponseHeaders = accountMetric(start, end, d.TotalMS)
			}
		}
		if out.Mode == "stream" {
			if first, ok := d.Events["account_first_content"]; ok {
				out.FirstContent = accountMetric(a.StartMS, first, d.TotalMS)
				if terminal, ok := d.Events["account_stream_end"]; ok {
					out.StreamOutput = accountMetric(first, terminal, d.TotalMS)
				}
			} else {
				out.StreamOutput = missingAccountMetric("no_content")
			}
		}
	} else if len(attempts) > 1 {
		out.Connection, out.ResponseHeaders = missingAccountMetric("multiple_requests"), missingAccountMetric("multiple_requests")
		out.FirstContent, out.StreamOutput = missingAccountMetric("multiple_requests"), missingAccountMetric("multiple_requests")
	}
	if out.Mode == "non_stream" {
		out.FirstContent, out.StreamOutput = missingAccountMetric("non_stream"), missingAccountMetric("non_stream")
	}
	return out
}
