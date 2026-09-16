-- 716 down: 恢复探测健康视图族到旧数据源(model_probe_state / model_probe_runs)
-- 即 716 之前的定义(与 db.go 旧内嵌 SQL 一致)。

DROP VIEW IF EXISTS v_model_health_dashboard CASCADE;
DROP VIEW IF EXISTS v_probe_queue_snapshot CASCADE;
DROP VIEW IF EXISTS v_model_priority_details CASCADE;
DROP VIEW IF EXISTS v_probe_system_health CASCADE;
DROP VIEW IF EXISTS v_model_availability_timeline CASCADE;
DROP VIEW IF EXISTS v_node_probe_state_compat CASCADE;
DROP FUNCTION IF EXISTS get_model_state_summary(TEXT) CASCADE;

CREATE OR REPLACE VIEW v_model_health_dashboard AS
WITH model_stats AS (
    SELECT
        mps.raw_model_name,
        mps.raw_model_name as outbound_model_name,
        'openai-completions' as protocol,
        p.display_name as provider_name,
        COUNT(*) as total_credentials,
        COUNT(*) FILTER (WHERE mps.state IN ('healthy_confirmed', 'healthy')) as healthy_count,
        COUNT(*) FILTER (WHERE mps.state = 'suspicious') as suspicious_count,
        COUNT(*) FILTER (WHERE mps.state IN ('failing', 'recovering')) as failing_count,
        COUNT(*) FILTER (WHERE mps.state = 'probing') as probing_count,
        SUM(CASE WHEN mps.consecutive_failures >= 3 THEN 1 ELSE 0 END) as urgent_count,
        COUNT(*) FILTER (WHERE mps.state = 'suspicious') as suspicious_priority_count,
        COUNT(*) FILTER (WHERE mps.state IN ('failing', 'recovering')) as failing_priority_count,
        COUNT(*) FILTER (WHERE mps.state = 'healthy_confirmed') as watchdog_count,
        AVG(CASE WHEN mps.total_attempts > 0
            THEN mps.consecutive_successes::float / mps.total_attempts * 100
            ELSE NULL END) as avg_success_rate_7d,
        AVG(EXTRACT(EPOCH FROM (mps.next_retry_at - NOW())) / 3600) as avg_verification_hours,
        AVG(mps.consecutive_successes) as avg_consecutive_successes,
        0 as total_real_success_24h,
        0 as total_real_failure_24h,
        MAX(mps.last_attempt_at) as last_verified_at,
        MAX(mps.last_attempt_at) as last_real_request_at,
        MIN(mps.next_retry_at) as next_probe_at,
        SUM(CASE WHEN mps.state IN ('failing', 'broken_confirmed')
                  AND mps.consecutive_failures >= 3
             THEN 1 ELSE 0 END) as critical_nodes,
        COUNT(*) FILTER (
            WHERE mps.next_retry_at <= NOW() + INTERVAL '5 minutes'
              AND mps.state != 'probing'
        ) as pending_probes_5min
    FROM model_probe_state mps
    JOIN credentials c ON c.id = mps.credential_id
    JOIN providers p ON p.id = c.provider_id
    WHERE COALESCE(c.status, 'active') = 'active'
      AND COALESCE(c.lifecycle_status, 'active') = 'active'
      AND COALESCE(c.manual_disabled, FALSE) = FALSE
    GROUP BY mps.raw_model_name, p.display_name
)
SELECT
    0 as provider_model_id,
    raw_model_name,
    outbound_model_name,
    protocol,
    provider_name,
    total_credentials,
    healthy_count,
    suspicious_count,
    failing_count,
    probing_count,
    ROUND(healthy_count * 100.0 / NULLIF(total_credentials, 0), 1) as healthy_percentage,
    ROUND(failing_count * 100.0 / NULLIF(total_credentials, 0), 1) as failing_percentage,
    urgent_count,
    suspicious_priority_count,
    failing_priority_count,
    watchdog_count,
    ROUND(avg_success_rate_7d::numeric, 2) as avg_success_rate_7d,
    ROUND(avg_verification_hours::numeric, 1) as avg_verification_hours,
    ROUND(avg_consecutive_successes::numeric, 1) as avg_consecutive_successes,
    total_real_success_24h,
    total_real_failure_24h,
    CASE
        WHEN (total_real_success_24h + total_real_failure_24h) > 0
        THEN ROUND((total_real_success_24h * 100.0 / (total_real_success_24h + total_real_failure_24h))::numeric, 2)
        ELSE NULL
    END as real_success_rate_24h,
    last_verified_at,
    last_real_request_at,
    next_probe_at,
    critical_nodes,
    pending_probes_5min,
    CASE
        WHEN critical_nodes > 0 THEN 'critical'
        WHEN ROUND(failing_count * 100.0 / NULLIF(total_credentials, 0), 1) > 20 THEN 'warning'
        WHEN ROUND(failing_count * 100.0 / NULLIF(total_credentials, 0), 1) > 10 THEN 'degraded'
        WHEN ROUND(healthy_count * 100.0 / NULLIF(total_credentials, 0), 1) >= 90 THEN 'healthy'
        ELSE 'normal'
    END as overall_health
FROM model_stats;

CREATE OR REPLACE VIEW v_probe_queue_snapshot AS
SELECT
    sub.probe_priority,
    sub.state,
    COUNT(*) as queue_size,
    COUNT(*) FILTER (WHERE sub.next_retry_at <= NOW()) as ready_now,
    COUNT(*) FILTER (WHERE sub.next_retry_at <= NOW() + INTERVAL '1 minute') as ready_1min,
    COUNT(*) FILTER (WHERE sub.next_retry_at <= NOW() + INTERVAL '5 minutes') as ready_5min,
    MIN(sub.next_retry_at) as earliest_retry_at,
    MAX(sub.next_retry_at) as latest_retry_at,
    AVG(EXTRACT(EPOCH FROM (NOW() - sub.last_attempt_at))) as avg_wait_seconds,
    MAX(EXTRACT(EPOCH FROM (NOW() - sub.last_attempt_at))) as max_wait_seconds
FROM (
    SELECT
    CASE
        WHEN mps.consecutive_failures >= 3 THEN 'urgent'
        WHEN mps.state = 'suspicious' THEN 'suspicious'
        WHEN mps.state IN ('failing', 'recovering') THEN 'failing'
        WHEN mps.state = 'healthy_confirmed' THEN 'watchdog'
        ELSE NULL
    END as probe_priority,
        mps.state,
        mps.next_retry_at,
        mps.last_attempt_at
    FROM model_probe_state mps
    JOIN credentials c ON c.id = mps.credential_id
    WHERE mps.state IN ('suspicious', 'failing', 'recovering')
      AND COALESCE(c.status, 'active') = 'active'
      AND COALESCE(c.lifecycle_status, 'active') = 'active'
      AND COALESCE(c.manual_disabled, FALSE) = FALSE
) sub
GROUP BY sub.probe_priority, sub.state;

CREATE OR REPLACE VIEW v_probe_system_health AS
SELECT
    (SELECT COUNT(*) FROM model_probe_state) as total_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state IN ('healthy_confirmed', 'healthy')) as healthy_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state IN ('failing', 'broken_confirmed')) as failing_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'suspicious') as suspicious_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'probing') as probing_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE consecutive_failures >= 3) as urgent_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'suspicious') as suspicious_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE state IN ('failing', 'recovering')) as failing_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'healthy_confirmed') as watchdog_queue_size,
    (SELECT COUNT(*) FROM model_probe_state
     WHERE next_retry_at <= NOW() AND state != 'probing') as ready_probes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'probing') as current_probing,
    (SELECT COUNT(DISTINCT credential_id) FROM model_probe_state
     WHERE state = 'probing') as credentials_being_probed,
    (SELECT ROUND(AVG(CASE WHEN total_attempts > 0
                           THEN consecutive_successes::float / total_attempts * 100
                           ELSE NULL END)::numeric, 2)
     FROM model_probe_state) as avg_success_rate_7d,
    (SELECT MAX(last_attempt_at) FROM model_probe_state) as last_probe_at,
    (SELECT MAX(last_attempt_at) FROM model_probe_state) as last_real_request_at,
    0 as total_real_success_24h,
    0 as total_real_failure_24h,
    (SELECT COUNT(*) FROM model_probe_state
     WHERE state IN ('failing', 'broken_confirmed')
       AND consecutive_failures >= 5) as critical_nodes,
    (SELECT COUNT(*) FROM model_probe_state
     WHERE next_retry_at <= NOW() + INTERVAL '5 minutes'
       AND state != 'probing') as pending_probes_5min,
    NOW() as snapshot_at;
