package quality

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

var minuteLock sync.Mutex

func collectMinuteMetrics(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	minuteLock.Lock()
	defer minuteLock.Unlock()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	query := `
INSERT INTO provider_metrics_minute (
    provider_id, model_name, endpoint, bucket,
    total_requests, successful_requests,
    error_5xx, error_4xx, error_timeout, error_other,
    latency_sum, latency_min, latency_max,
    latency_p50, latency_p95, latency_p99,
    ttft_sum, ttft_p95,
    total_input_tokens, total_output_tokens, total_cost
)
SELECT
    provider_id,
    COALESCE(NULLIF(outbound_model, ''), NULLIF(client_model, ''), 'unknown') AS model_name,
    'unknown' AS endpoint,
    date_trunc('minute', ts) AS bucket,
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (WHERE upstream_status_code BETWEEN 200 AND 299 OR (upstream_status_code IS NULL AND success)) AS successful_requests,
    COUNT(*) FILTER (WHERE upstream_status_code BETWEEN 500 AND 599) AS error_5xx,
    COUNT(*) FILTER (WHERE upstream_status_code BETWEEN 400 AND 499) AS error_4xx,
    COUNT(*) FILTER (WHERE error_kind = 'timeout') AS error_timeout,
    COUNT(*) FILTER (WHERE (upstream_status_code IS NOT NULL AND upstream_status_code NOT BETWEEN 200 AND 599 AND error_kind <> 'timeout')
                       OR (upstream_status_code IS NULL AND NOT success AND COALESCE(error_kind, '') <> 'timeout')) AS error_other,
    SUM(COALESCE(latency_ms, 0))::BIGINT AS latency_sum,
    MIN(latency_ms) AS latency_min,
    MAX(latency_ms) AS latency_max,
    PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY latency_ms) AS latency_p50,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms) AS latency_p95,
    PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY latency_ms) AS latency_p99,
    SUM(COALESCE(stream_first_chunk_ms, 0))::BIGINT AS ttft_sum,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY stream_first_chunk_ms) FILTER (WHERE stream_first_chunk_ms IS NOT NULL) AS ttft_p95,
    SUM(COALESCE(prompt_tokens, 0))::BIGINT AS total_input_tokens,
    SUM(COALESCE(completion_tokens, 0))::BIGINT AS total_output_tokens,
    SUM(COALESCE(cost_usd, 0)) AS total_cost
FROM request_logs_hot
WHERE ts >= date_trunc('minute', NOW() - INTERVAL '1 minute')
  AND ts < date_trunc('minute', NOW())
  AND provider_id IS NOT NULL
  AND provider_id > 0
GROUP BY provider_id, COALESCE(NULLIF(outbound_model, ''), NULLIF(client_model, ''), 'unknown'), date_trunc('minute', ts)
ON CONFLICT (provider_id, model_name, endpoint, bucket) DO UPDATE SET
    total_requests = EXCLUDED.total_requests,
    successful_requests = EXCLUDED.successful_requests,
    error_5xx = EXCLUDED.error_5xx,
    error_4xx = EXCLUDED.error_4xx,
    error_timeout = EXCLUDED.error_timeout,
    error_other = EXCLUDED.error_other,
    latency_sum = EXCLUDED.latency_sum,
    latency_min = EXCLUDED.latency_min,
    latency_max = EXCLUDED.latency_max,
    latency_p50 = EXCLUDED.latency_p50,
    latency_p95 = EXCLUDED.latency_p95,
    latency_p99 = EXCLUDED.latency_p99,
    ttft_sum = EXCLUDED.ttft_sum,
    ttft_p95 = EXCLUDED.ttft_p95,
    total_input_tokens = EXCLUDED.total_input_tokens,
    total_output_tokens = EXCLUDED.total_output_tokens,
    total_cost = EXCLUDED.total_cost;
`

	_, err := db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("分钟级聚合失败: %w", err)
	}
	return nil
}
