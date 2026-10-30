package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Both usage and ops use the same SQL population. Physical HTTP samples are
// counted independently of egress parents. Missing traces and boundaries remain
// in the request count without contributing a zero latency sample.
func latencyReportSQL(f service.LatencyReportFilter) (string, []any) {
	args := []any{f.Start, f.End}
	where := "u.created_at >= $1 AND u.created_at < $2"
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"user_id", f.UserID}, {"api_key_id", f.APIKeyID}, {"account_id", f.AccountID}, {"group_id", f.GroupID},
	} {
		if field.value > 0 {
			args = append(args, field.value)
			where += fmt.Sprintf(" AND u.%s=$%d", field.name, len(args))
		}
	}
	if f.Model != "" {
		args = append(args, f.Model)
		where += fmt.Sprintf(" AND COALESCE(NULLIF(u.requested_model,''),u.model)=$%d", len(args))
	}
	return `WITH traces AS (
 SELECT u.id, COALESCE(NULLIF(u.reasoning_effort,''),'unknown') effort,
 CASE WHEN u.input_tokens+u.cache_read_tokens<=0 THEN 'unknown'
 WHEN u.cache_read_tokens::numeric/(u.input_tokens::numeric+u.cache_read_tokens)<.8 THEN '<80%'
 WHEN u.cache_read_tokens::numeric/(u.input_tokens::numeric+u.cache_read_tokens)<.95 THEN '80-95%' ELSE '95%+' END cache,
 d.detail,d.trace_id,
 CASE WHEN jsonb_typeof(d.detail->'body_bytes')='number' THEN (d.detail->>'body_bytes')::numeric END body
 FROM usage_logs u LEFT JOIN LATERAL (
  SELECT detail,trace_id FROM request_timing_details WHERE usage_log_id=u.id ORDER BY created_at DESC,trace_id LIMIT 20
 ) d ON true WHERE ` + where + `
), grouped_traces AS (
 SELECT *,CASE WHEN body IS NULL THEN 'unknown' WHEN body<65536 THEN '<64KiB'
 WHEN body<262144 THEN '64-256KiB' WHEN body<=1048576 THEN '256KiB-1MiB'
 WHEN body<=4194304 THEN '1-4MiB' ELSE '4MiB+' END body_bucket FROM traces
), trace_counts AS (
 SELECT body_bucket,cache,effort,COUNT(DISTINCT id) requests,COUNT(trace_id) traces,
 COUNT(*) FILTER (WHERE detail->'latency'->>'large_body'='true' AND detail->'latency'->>'status'='measured') failover_samples,
 SUM(CASE WHEN detail->'latency'->>'large_body'='true' THEN COALESCE((detail->'latency'->>'account_switches')::bigint,0) ELSE 0 END) switches,
 SUM(CASE WHEN detail->'latency'->>'large_body'='true' THEN COALESCE((detail->'latency'->>'same_account_retries')::bigint,0) ELSE 0 END) retries,
 COUNT(*) FILTER (WHERE detail->'latency'->>'cost_status'='measured_components') costs,
 SUM((detail->'latency'->>'extra_upload_component_ms')::float8) extra_upload,
 SUM((detail->'latency'->>'extra_wait_component_ms')::float8) extra_wait
 FROM grouped_traces GROUP BY body_bucket,cache,effort
), attempts AS (
 SELECT t.*,a.value a FROM grouped_traces t CROSS JOIN LATERAL jsonb_array_elements(
 CASE WHEN jsonb_typeof(detail->'attempts')='array' THEN detail->'attempts' ELSE '[]'::jsonb END) a
 WHERE a.value->>'kind'='http'
), boundaries AS (
 SELECT *,CASE WHEN jsonb_typeof(a->'start_ms')='number' THEN (a->>'start_ms')::numeric END started,
 CASE WHEN jsonb_typeof(a->'end_ms')='number' THEN (a->>'end_ms')::numeric END ended,
 CASE WHEN jsonb_typeof(a->'events'->'headers_written')='number' THEN (a->'events'->>'headers_written')::numeric END headers,
 CASE WHEN jsonb_typeof(a->'events'->'request_written')='number' THEN (a->'events'->>'request_written')::numeric
 WHEN jsonb_typeof(a->'events'->'wrote_request')='number' THEN (a->'events'->>'wrote_request')::numeric END written,
 CASE WHEN jsonb_typeof(a->'events'->'first_semantic')='number' THEN (a->'events'->>'first_semantic')::numeric END semantic,
 CASE WHEN jsonb_typeof(a->'events'->'first_visible')='number' THEN (a->'events'->>'first_visible')::numeric END visible
 FROM attempts
), measured AS (
 SELECT *,CASE WHEN started>=0 AND headers>=started AND written>=headers AND ended>=written AND NOT (a->'events' ? 'request_write_failed') THEN written-headers END upload
 FROM boundaries
), samples AS (
 SELECT body_bucket,cache,effort,COUNT(*) attempts,COUNT(upload) uploads,AVG(upload)::float8 upload_avg,
 COUNT(*) FILTER (WHERE upload IS NOT NULL AND semantic>=written AND ended>=semantic) waits,
 AVG(CASE WHEN upload IS NOT NULL AND semantic>=written AND ended>=semantic THEN semantic-written END)::float8 wait_avg,
 COUNT(*) FILTER (WHERE upload IS NOT NULL AND visible>=written AND ended>=visible) visibles,
 AVG(CASE WHEN upload IS NOT NULL AND visible>=written AND ended>=visible THEN visible-written END)::float8 visible_avg
 FROM measured GROUP BY body_bucket,cache,effort
)
 SELECT c.body_bucket,c.cache,c.effort,c.requests,c.traces,COALESCE(s.attempts,0),COALESCE(s.uploads,0),COALESCE(s.waits,0),COALESCE(s.visibles,0),
 s.upload_avg,s.wait_avg,s.visible_avg,COALESCE(c.switches,0),c.failover_samples,COALESCE(c.retries,0),c.costs,c.extra_upload,c.extra_wait
 FROM trace_counts c LEFT JOIN samples s USING(body_bucket,cache,effort) ORDER BY c.body_bucket,c.cache,c.effort LIMIT 1000`, args
}

func readLatencyGroups(ctx context.Context, sql sqlExecutor, f service.LatencyReportFilter) ([]service.LatencyGroup, error) {
	query, args := latencyReportSQL(f)
	rows, err := sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.LatencyGroup{}
	for rows.Next() {
		var v service.LatencyGroup
		if err := rows.Scan(&v.BodyBucket, &v.CacheBucket, &v.ReasoningEffort, &v.Requests, &v.Traces, &v.Attempts, &v.UploadSamples, &v.WaitSamples, &v.VisibleSamples, &v.UploadAvgMS, &v.WaitAvgMS, &v.VisibleAvgMS, &v.LargeBodySwitches, &v.FailoverSamples, &v.LargeBodyRetries, &v.CostSamples, &v.ExtraUploadMS, &v.ExtraWaitMS); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *usageLogRepository) ReadLatencyReport(ctx context.Context, f service.LatencyReportFilter) ([]service.LatencyGroup, error) {
	return readLatencyGroups(ctx, r.sql, f)
}
func (r *opsRepository) ReadLatencyReport(ctx context.Context, f service.LatencyReportFilter) ([]service.LatencyGroup, error) {
	return readLatencyGroups(ctx, r.db, f)
}
