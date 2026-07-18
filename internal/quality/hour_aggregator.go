package quality

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

var hourLock sync.Mutex

func collectHourMetrics(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	hourLock.Lock()
	defer hourLock.Unlock()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	query := `
INSERT INTO provider_metrics_hour (
    provider_id, model_name, endpoint, bucket,
    total_requests, successful_requests,
    error_5xx, error_4xx, error_timeout,
    success_rate, error_rate_5xx,
    latency_p50, latency_p95, latency_p99, ttft_p95,
    total_input_tokens, total_output_tokens, total_cost
)
SELECT
    provider_id, model_name, endpoint,
    date_trunc('hour', bucket) as bucket,
    SUM(total_requests)::BIGINT as total_requests,
    SUM(successful_requests)::BIGINT as successful_requests,
    SUM(error_5xx) as error_5xx,
    SUM(error_4xx) as error_4xx,
    SUM(error_timeout) as error_timeout,
    CASE 
        WHEN SUM(total_requests) > 0 
        THEN (100.0 * SUM(successful_requests) / SUM(total_requests))::DECIMAL(5,2)
        ELSE NULL
    END as success_rate,
    CASE 
        WHEN SUM(total_requests) > 0 
        THEN (100.0 * SUM(error_5xx) / SUM(total_requests))::DECIMAL(5,2)
        ELSE NULL
    END as error_rate_5xx,
    AVG(latency_p50) as latency_p50,
    AVG(latency_p95) as latency_p95,
    AVG(latency_p99) as latency_p99,
    AVG(ttft_p95) as ttft_p95,
    SUM(total_input_tokens)::BIGINT as total_input_tokens,
    SUM(total_output_tokens)::BIGINT as total_output_tokens,
    SUM(total_cost) as total_cost
FROM provider_metrics_minute
WHERE bucket >= date_trunc('hour', NOW() - INTERVAL '1 hour')
  AND bucket < date_trunc('hour', NOW())
GROUP BY provider_id, model_name, endpoint, date_trunc('hour', bucket)
ON CONFLICT (provider_id, model_name, endpoint, bucket) DO UPDATE SET
    total_requests = EXCLUDED.total_requests,
    successful_requests = EXCLUDED.successful_requests,
    error_5xx = EXCLUDED.error_5xx,
    error_4xx = EXCLUDED.error_4xx,
    error_timeout = EXCLUDED.error_timeout,
    success_rate = EXCLUDED.success_rate,
    error_rate_5xx = EXCLUDED.error_rate_5xx,
    latency_p50 = EXCLUDED.latency_p50,
    latency_p95 = EXCLUDED.latency_p95,
    latency_p99 = EXCLUDED.latency_p99,
    ttft_p95 = EXCLUDED.ttft_p95,
    total_input_tokens = EXCLUDED.total_input_tokens,
    total_output_tokens = EXCLUDED.total_output_tokens,
    total_cost = EXCLUDED.total_cost;
`

	_, err := db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("小时级聚合失败: %w", err)
	}
	return nil
}
