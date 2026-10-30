package service

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type LatencyReportFilter struct {
	Start           time.Time
	End             time.Time
	UserID          int64
	APIKeyID        int64
	AccountID       int64
	GroupID         int64
	Model           string
	Platform        string
	IncludeUnbilled bool
}

type LatencyGroup struct {
	BodyBucket        string   `json:"body_bucket"`
	CacheBucket       string   `json:"cache_bucket"`
	ReasoningEffort   string   `json:"reasoning_effort"`
	Requests          int64    `json:"requests"`
	Traces            int64    `json:"traces"`
	Attempts          int64    `json:"attempts"`
	UploadSamples     int64    `json:"upload_samples"`
	WaitSamples       int64    `json:"wait_samples"`
	VisibleSamples    int64    `json:"visible_samples"`
	UploadAvgMS       *float64 `json:"body_upload_avg_ms"`
	WaitAvgMS         *float64 `json:"post_upload_first_content_wait_avg_ms"`
	VisibleAvgMS      *float64 `json:"post_upload_first_visible_wait_avg_ms"`
	LargeBodySwitches int64    `json:"large_body_switches"`
	FailoverSamples   int64    `json:"failover_samples"`
	LargeBodyRetries  int64    `json:"large_body_same_account_retries"`
	CostSamples       int64    `json:"cost_samples"`
	ExtraUploadMS     *float64 `json:"extra_upload_component_ms"`
	ExtraWaitMS       *float64 `json:"extra_wait_component_ms"`
}

type LatencyReport struct {
	Groups             []LatencyGroup `json:"groups"`
	EndToEndMS         *float64       `json:"end_to_end_ms"`
	EndToEndStatus     string         `json:"end_to_end_status"`
	EndToEndSource     string         `json:"end_to_end_source"`
	ClientUploadMS     *float64       `json:"client_upload_ms"`
	ClientUploadStatus string         `json:"client_upload_status"`
	Scope              string         `json:"scope"`
}

type latencyReportReader interface {
	ReadLatencyReport(context.Context, LatencyReportFilter) ([]LatencyGroup, error)
}

func readLatencyReport(ctx context.Context, reader any, f LatencyReportFilter) (*LatencyReport, error) {
	if !f.End.After(f.Start) || f.End.Sub(f.Start) > 31*24*time.Hour {
		return nil, infraerrors.BadRequest("LATENCY_RANGE_INVALID", "Latency range must be positive and at most 31 days")
	}
	r, ok := reader.(latencyReportReader)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("LATENCY_UNAVAILABLE", "Latency reporting unavailable")
	}
	groups, err := r.ReadLatencyReport(ctx, f)
	if err != nil {
		return nil, err
	}
	return &LatencyReport{Groups: groups, EndToEndStatus: "not_collected", EndToEndSource: "none", ClientUploadStatus: "not_collected", Scope: "application_http_attempts_components_not_wall_time"}, nil
}

func (s *UsageService) LatencyReport(ctx context.Context, f LatencyReportFilter) (*LatencyReport, error) {
	return readLatencyReport(ctx, s.usageRepo, f)
}

func (s *OpsService) LatencyReport(ctx context.Context, f LatencyReportFilter) (*LatencyReport, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	f.IncludeUnbilled = true
	return readLatencyReport(ctx, s.opsRepo, f)
}
