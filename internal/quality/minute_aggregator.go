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
    model_name,
    COALESCE(endpoint, 'unknown') as endpoint,
    date_trunc('minute', created_at) as bucket,
    COUNT(*) as total_requests,
    COUNT(*) FILTER (WHERE status_code BETWEEN 200 AND 299) as successful_requests,
    COUNT(*) FILTER (WHERE status_code BETWEEN 500 AND 599) as error_5xx,
    COUNT(*) FILTER (WHERE status_code BETWEEN 400 AND 499) as error_4xx,
    COUNT(*) FILTER (WHERE error_type = 'timeout') as error_timeout,
    COUNT(*) FILTER (WHERE status_code NOT BETWEEN 200 AND 599 AND error_type != 'timeout') as error_other,
    SUM(COALESCE(latency_ms, 0))::BIGINT as latency_sum,
    MIN(latency_ms) as latency_min,
    MAX(latency_ms) as latency_max,
    PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY latency_ms) as latency_p50,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms) as latency_p95,
    PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY latency_ms) as latency_p99,
    SUM(COALESCE(ttft_ms, 0))::BIGINT as ttft_sum,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY ttft_ms) FILTER (WHERE ttft_ms IS NOT NULL) as ttft_p95,
    SUM(COALESCE(input_tokens, 0))::BIGINT as total_input_tokens,
    SUM(COALESCE(output_tokens, 0))::BIGINT as total_output_tokens,
    SUM(COALESCE(cost, 0)) as total_cost
FROM request_logs
WHERE created_at >= date_trunc('minute', NOW() - INTERVAL '1 minute')
  AND created_at < date_trunc('minute', NOW())
  AND provider_id IS NOT NULL
GROUP BY provider_id, model_name, endpoint, date_trunc('minute', created_at)
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
