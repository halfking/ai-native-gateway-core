-- 716: 统一探测健康展示面到新探测系统(node_probe_state)
--
-- 背景(2026-09-17):probe-health 页(v_model_health_dashboard 视图族)与凭据
-- 列表 probe_state 读旧 model_probe_state,该表在 LLM_GATEWAY_USE_NEW_PROBE_MODE
-- =true(默认)下停更,状态被冻结(如 NVIDIA NIM minimax-m3 显示 3/3 healthy,
-- last_verified 停在 2026-07-20),而热力图(request_logs)与路由
-- (node_probe_state)显示/执行的是真实失败(http_410/403)——两页数据不一致。
--
-- 本迁移把 5 个探测健康视图 + get_model_state_summary() 全部切到
-- v_node_probe_state_compat(node_probe_state 的旧词汇投影),并新增该兼容视图。
-- 热力图 /api/credentials/heatmap 另由代码附带 node_status(同一事实源)。
--
-- 闭环对齐:
--   探测更新节点状态(node_probe_state) → 热力图展示节点状态(node_status)
--   → 路由消费节点状态(autoroute SQL 原有) → 请求结果回写节点状态
--   (MarkNodeProbeHealthy / request_failure 探测,原有)。
--
-- 幂等:与 db.ensureProbeHealthDashboardViews 相同定义,网关每次启动也会
-- DROP+重建。列顺序与旧契约完全一致(admin/probe_dashboard.go SELECT *
-- 按位置 Scan)。

DROP VIEW IF EXISTS v_model_health_dashboard CASCADE;
DROP VIEW IF EXISTS v_probe_queue_snapshot CASCADE;
DROP VIEW IF EXISTS v_model_priority_details CASCADE;
DROP VIEW IF EXISTS v_probe_system_health CASCADE;
DROP VIEW IF EXISTS v_model_availability_timeline CASCADE;
DROP VIEW IF EXISTS v_node_probe_state_compat CASCADE;
DROP FUNCTION IF EXISTS get_model_state_summary(TEXT) CASCADE;

CREATE OR REPLACE VIEW v_node_probe_state_compat AS
SELECT
    nps.credential_id,
    nps.raw_model_name,
    CASE
        WHEN nps.paused = TRUE AND nps.last_err_code = 'manual_offline' THEN 'unknown'
        WHEN nps.in_flight_until IS NOT NULL AND nps.in_flight_until > NOW() THEN 'probing'
        WHEN nps.last_direct_ok IS NULL THEN 'probing'
        WHEN nps.last_direct_ok THEN 'healthy_confirmed'
        WHEN nps.consecutive_failures >= 3 THEN 'broken_confirmed'
        ELSE 'suspicious'
    END AS state,
    CASE
        WHEN nps.consecutive_failures >= 3 THEN 'urgent'
        WHEN nps.consecutive_failures >= 1 THEN 'suspicious'
        ELSE 'watchdog'
    END AS probe_priority,
    nps.consecutive_successes,
    nps.consecutive_failures,
    (nps.consecutive_successes + nps.consecutive_failures) AS total_attempts,
    nps.last_attempt_at,
    nps.next_retry_at,
    nps.last_err_code AS last_status,
    nps.updated_at AS last_state_change_at,
    nps.last_run_id AS last_state_change_run,
    nps.paused,
    nps.in_flight_until,
    nps.last_direct_ok,
    nps.last_gateway_ok,
    nps.last_err_detail,
    nps.updated_at
FROM node_probe_state nps;

CREATE OR REPLACE VIEW v_model_health_dashboard AS
WITH real24 AS (
    SELECT
        rl.credential_id,
        lower(COALESCE(rl.outbound_model, rl.client_model)) AS model_name,
        COUNT(*) FILTER (WHERE rl.success) AS ok_24h,
        COUNT(*) FILTER (WHERE NOT rl.success) AS fail_24h,
        MAX(rl.ts) AS last_real_request_at
    FROM request_logs_with_current_month rl
    WHERE rl.ts > NOW() - INTERVAL '24 hours'
      AND COALESCE(rl.task_type, '') <> 'probe_triggered'
      AND NOT ('probe' = ANY(rl.quality_flags))
    GROUP BY rl.credential_id, lower(COALESCE(rl.outbound_model, rl.client_model))
),
model_stats AS (
    SELECT
        mps.raw_model_name,
        mps.raw_model_name as outbound_model_name,
        'openai-completions' as protocol,
        p.display_name as provider_name,

        COUNT(*) as total_credentials,
        COUNT(*) FILTER (WHERE mps.state IN ('healthy_confirmed', 'healthy')) as healthy_count,
        COUNT(*) FILTER (WHERE mps.state = 'suspicious') as suspicious_count,
        COUNT(*) FILTER (WHERE mps.state IN ('failing', 'recovering', 'broken_confirmed')) as failing_count,
        COUNT(*) FILTER (WHERE mps.state = 'probing') as probing_count,

        SUM(CASE WHEN mps.consecutive_failures >= 3 THEN 1 ELSE 0 END) as urgent_count,
        COUNT(*) FILTER (WHERE mps.state = 'suspicious') as suspicious_priority_count,
        COUNT(*) FILTER (WHERE mps.state IN ('failing', 'recovering', 'broken_confirmed')) as failing_priority_count,
        COUNT(*) FILTER (WHERE mps.state = 'healthy_confirmed') as watchdog_count,

        AVG(CASE WHEN mps.total_attempts > 0
            THEN mps.consecutive_successes::float / mps.total_attempts * 100
            ELSE NULL END) as avg_success_rate_7d,
        AVG(EXTRACT(EPOCH FROM (mps.next_retry_at - NOW())) / 3600) as avg_verification_hours,
        AVG(mps.consecutive_successes) as avg_consecutive_successes,

        COALESCE(SUM(real24.ok_24h), 0) as total_real_success_24h,
        COALESCE(SUM(real24.fail_24h), 0) as total_real_failure_24h,

        MAX(mps.last_attempt_at) as last_verified_at,
        MAX(real24.last_real_request_at) as last_real_request_at,
        MIN(mps.next_retry_at) as next_probe_at,

        SUM(CASE WHEN mps.state IN ('failing', 'recovering', 'broken_confirmed')
                  AND mps.consecutive_failures >= 3
             THEN 1 ELSE 0 END) as critical_nodes,

        COUNT(*) FILTER (
            WHERE mps.next_retry_at <= NOW() + INTERVAL '5 minutes'
              AND mps.state != 'probing'
              AND mps.paused = FALSE
        ) as pending_probes_5min

    FROM v_node_probe_state_compat mps
    JOIN credentials c ON c.id = mps.credential_id
    JOIN providers p ON p.id = c.provider_id
    LEFT JOIN real24
      ON real24.credential_id = mps.credential_id
     AND real24.model_name = lower(mps.raw_model_name)
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
FROM model_stats
ORDER BY
    CASE
        WHEN critical_nodes > 0 THEN 1
        WHEN urgent_count > 0 THEN 2
        WHEN ROUND(failing_count * 100.0 / NULLIF(total_credentials, 0), 1) > 20 THEN 3
        ELSE 4
    END,
    total_credentials DESC,
    raw_model_name;

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
        WHEN mps.state = 'broken_confirmed' THEN 'failing'
        ELSE NULL
    END as probe_priority,
        mps.state,
        mps.next_retry_at,
        mps.last_attempt_at
    FROM v_node_probe_state_compat mps
    JOIN credentials c ON c.id = mps.credential_id
    WHERE mps.state IN ('suspicious', 'broken_confirmed')
      AND mps.paused = FALSE
      AND COALESCE(c.status, 'active') = 'active'
      AND COALESCE(c.lifecycle_status, 'active') = 'active'
      AND COALESCE(c.manual_disabled, FALSE) = FALSE
) sub
GROUP BY sub.probe_priority, sub.state
ORDER BY
    CASE
        WHEN sub.probe_priority = 'urgent' THEN 1
        WHEN sub.probe_priority = 'suspicious' THEN 2
        WHEN sub.probe_priority = 'failing' THEN 3
        WHEN sub.probe_priority = 'watchdog' THEN 4
        ELSE 5
    END,
    sub.state;

CREATE OR REPLACE VIEW v_model_priority_details AS
SELECT
    mps.raw_model_name,
    mps.raw_model_name as outbound_model_name,
    CASE
        WHEN mps.consecutive_failures >= 3 THEN 'urgent'
        WHEN mps.state = 'suspicious' THEN 'suspicious'
        WHEN mps.state = 'broken_confirmed' THEN 'failing'
        ELSE 'watchdog'
    END as probe_priority,
    mps.state,
    c.id as credential_id,
    c.label as credential_label,
    p.display_name as provider_name,
    mps.last_attempt_at as last_verified_at,
    mps.next_retry_at,
    mps.updated_at as marked_suspicious_at,
    CASE WHEN mps.in_flight_until > NOW() THEN mps.in_flight_until END as probing_started_at,
    mps.consecutive_successes,
    mps.consecutive_failures,
    0 as consecutive_watchdog_successes,
    CASE WHEN mps.total_attempts > 0
         THEN mps.consecutive_successes::float / mps.total_attempts * 100
         ELSE NULL END as success_rate_7d,
    (mps.next_retry_at - NOW()) as verification_interval,
    0 as real_success_24h,
    0 as real_failure_24h,
    mps.last_attempt_at as last_real_request_at,
    mps.last_err_detail as last_unavailable_reason,
    mps.last_status as last_err_code,
    CASE
        WHEN mps.next_retry_at <= NOW() THEN 'ready'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '1 minute' THEN '<1min'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '5 minutes' THEN '<5min'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '1 hour' THEN '<1h'
        ELSE '>1h'
    END as retry_in,
    EXTRACT(EPOCH FROM (NOW() - mps.updated_at)) / 60 as state_duration_minutes
FROM v_node_probe_state_compat mps
JOIN credentials c ON c.id = mps.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
ORDER BY
    mps.raw_model_name,
    CASE
        WHEN mps.consecutive_failures >= 3 THEN 1
        WHEN mps.state = 'suspicious' THEN 2
        WHEN mps.state = 'broken_confirmed' THEN 3
        ELSE 4
    END,
    c.id;

CREATE OR REPLACE VIEW v_probe_system_health AS
SELECT
    (SELECT COUNT(*) FROM v_node_probe_state_compat) as total_nodes,
    (SELECT COUNT(*) FROM v_node_probe_state_compat WHERE state IN ('healthy_confirmed', 'healthy')) as healthy_nodes,
    (SELECT COUNT(*) FROM v_node_probe_state_compat WHERE state IN ('failing', 'broken_confirmed')) as failing_nodes,
    (SELECT COUNT(*) FROM v_node_probe_state_compat WHERE state = 'suspicious') as suspicious_nodes,
    (SELECT COUNT(*) FROM v_node_probe_state_compat WHERE state = 'probing') as probing_nodes,
    (SELECT COUNT(*) FROM v_node_probe_state_compat WHERE consecutive_failures >= 3) as urgent_queue_size,
    (SELECT COUNT(*) FROM v_node_probe_state_compat WHERE state = 'suspicious') as suspicious_queue_size,
    (SELECT COUNT(*) FROM v_node_probe_state_compat WHERE state IN ('failing', 'broken_confirmed')) as failing_queue_size,
    (SELECT COUNT(*) FROM v_node_probe_state_compat WHERE state = 'healthy_confirmed') as watchdog_queue_size,
    (SELECT COUNT(*) FROM v_node_probe_state_compat
     WHERE next_retry_at <= NOW() AND state != 'probing' AND paused = FALSE) as ready_probes,
    (SELECT COUNT(*) FROM v_node_probe_state_compat WHERE state = 'probing') as current_probing,
    (SELECT COUNT(DISTINCT credential_id) FROM v_node_probe_state_compat
     WHERE state = 'probing') as credentials_being_probed,
    (SELECT ROUND(AVG(CASE WHEN total_attempts > 0
                           THEN consecutive_successes::float / total_attempts * 100
                           ELSE NULL END)::numeric, 2)
     FROM v_node_probe_state_compat) as avg_success_rate_7d,
    (SELECT MAX(last_attempt_at) FROM v_node_probe_state_compat) as last_probe_at,
    (SELECT MAX(last_state_change_at) FROM v_node_probe_state_compat) as last_real_request_at,
    (SELECT COALESCE(SUM(ok_24h), 0) FROM (
         SELECT COUNT(*) FILTER (WHERE rl.success) AS ok_24h
         FROM request_logs_with_current_month rl
         WHERE rl.ts > NOW() - INTERVAL '24 hours'
           AND COALESCE(rl.task_type, '') <> 'probe_triggered'
           AND NOT ('probe' = ANY(rl.quality_flags))
     ) s) as total_real_success_24h,
    (SELECT COALESCE(SUM(fail_24h), 0) FROM (
         SELECT COUNT(*) FILTER (WHERE NOT rl.success) AS fail_24h
         FROM request_logs_with_current_month rl
         WHERE rl.ts > NOW() - INTERVAL '24 hours'
           AND COALESCE(rl.task_type, '') <> 'probe_triggered'
           AND NOT ('probe' = ANY(rl.quality_flags))
     ) s) as total_real_failure_24h,
    (SELECT COUNT(*) FROM v_node_probe_state_compat
     WHERE state IN ('failing', 'broken_confirmed')
       AND consecutive_failures >= 5) as critical_nodes,
    (SELECT COUNT(*) FROM v_node_probe_state_compat
     WHERE next_retry_at <= NOW() + INTERVAL '5 minutes'
       AND state != 'probing' AND paused = FALSE) as pending_probes_5min,
    NOW() as snapshot_at;

CREATE OR REPLACE VIEW v_model_availability_timeline AS
SELECT
    npr.raw_model_name,
    npr.raw_model_name as outbound_model_name,
    DATE_TRUNC('hour', npr.started_at) as hour_bucket,
    COUNT(*) as total_probes,
    COUNT(*) FILTER (WHERE npr.direct_ok) as successful_probes,
    COUNT(*) FILTER (WHERE NOT npr.direct_ok) as failed_probes,
    ROUND((COUNT(*) FILTER (WHERE npr.direct_ok) * 100.0 / COUNT(*))::numeric, 2) as success_rate,
    AVG(npr.direct_latency_ms) FILTER (WHERE npr.direct_ok) as avg_latency_ms,
    COUNT(DISTINCT npr.credential_id) as probed_credentials,
    COUNT(DISTINCT npr.credential_id) FILTER (WHERE npr.direct_ok) as successful_credentials,
    COUNT(DISTINCT npr.credential_id) FILTER (WHERE NOT npr.direct_ok) as failed_credentials
FROM node_probe_runs npr
WHERE npr.started_at >= NOW() - INTERVAL '24 hours'
GROUP BY npr.raw_model_name, DATE_TRUNC('hour', npr.started_at)
ORDER BY npr.raw_model_name, hour_bucket DESC;

CREATE OR REPLACE FUNCTION get_model_state_summary(p_raw_model_name TEXT)
RETURNS TABLE (
    state TEXT,
    priority TEXT,
    count BIGINT,
    avg_success_rate NUMERIC,
    next_probe_in_seconds INTEGER
)
LANGUAGE SQL
STABLE
AS $$
    SELECT
        sub.state::TEXT,
        sub.priority::TEXT,
        COUNT(*) as count,
        ROUND(AVG(CASE WHEN sub.total_attempts > 0
                       THEN sub.consecutive_successes::float / sub.total_attempts * 100
                       ELSE NULL END)::numeric, 2) as avg_success_rate,
        EXTRACT(EPOCH FROM MIN(sub.next_retry_at - NOW()))::INTEGER as next_probe_in_seconds
    FROM (
        SELECT
            mps.state,
            mps.consecutive_successes,
            mps.total_attempts,
            mps.next_retry_at,
            CASE
                WHEN mps.consecutive_failures >= 3 THEN 'urgent'
                WHEN mps.state = 'suspicious' THEN 'suspicious'
                WHEN mps.state = 'broken_confirmed' THEN 'failing'
                ELSE 'watchdog'
            END as priority
        FROM v_node_probe_state_compat mps
        JOIN credentials c ON c.id = mps.credential_id
        WHERE mps.raw_model_name = p_raw_model_name
          AND COALESCE(c.status, 'active') = 'active'
          AND COALESCE(c.lifecycle_status, 'active') = 'active'
          AND COALESCE(c.manual_disabled, FALSE) = FALSE
    ) sub
    GROUP BY sub.state, sub.priority
    ORDER BY
        CASE sub.priority
            WHEN 'urgent' THEN 1
            WHEN 'suspicious' THEN 2
            WHEN 'failing' THEN 3
            WHEN 'watchdog' THEN 4
            ELSE 5
        END,
        sub.state;
$$;
