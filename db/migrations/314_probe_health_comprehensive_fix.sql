-- Migration 314: Probe Health 修复汇总（comprehensive fix）
--
-- 编号历史（迁移链追溯）：
--   原编号 310 → 310 被 310_session_summaries 占用，改为 312
--   312        → 又与 312_output_compliance_monitoring 冲突，改为 314
-- 逻辑内容未变。
--
-- 目的：修复 probe-health 页面无数据问题 + 增强状态同步可靠性
--
-- 问题根因：
--   1. v_model_health_dashboard 等视图引用了不存在的表和字段
--   2. model_probe_state 缺少反向reconcile机制（healthy → available）
--
-- 修复内容：
--   1. 重建所有 probe dashboard 相关视图
--   2. Go代码已增加 reconcileHealthyConfirmedBindings() 反向同步
--
-- Spec: 2026-06-29-probe-health-fix

-- ── 1. 删除旧视图 ────────────────────────────────────────────────────────

DROP VIEW IF EXISTS v_model_health_dashboard CASCADE;
DROP VIEW IF EXISTS v_probe_queue_snapshot CASCADE;
DROP VIEW IF EXISTS v_model_priority_details CASCADE;
DROP VIEW IF EXISTS v_probe_system_health CASCADE;
DROP VIEW IF EXISTS v_model_availability_timeline CASCADE;
DROP FUNCTION IF EXISTS get_model_state_summary(TEXT) CASCADE;

-- ── 2. 重建视图（基于实际表结构）─────────────────────────────────────────

-- 2.1 模型健康度总览
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
        ELSE 'unknown'
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

-- 2.2 探测队列快照
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
            ELSE 'unknown'
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

-- 2.3 模型优先级详情
CREATE OR REPLACE VIEW v_model_priority_details AS
SELECT 
    mps.raw_model_name,
    mps.raw_model_name as outbound_model_name,
    CASE 
        WHEN mps.consecutive_failures >= 3 THEN 'urgent'
        WHEN mps.state = 'suspicious' THEN 'suspicious'
        WHEN mps.state IN ('failing', 'recovering') THEN 'failing'
        ELSE 'watchdog'
    END as probe_priority,
    mps.state,
    c.id as credential_id,
    c.label as credential_label,
    p.display_name as provider_name,
    mps.last_attempt_at as last_verified_at,
    mps.next_retry_at,
    mps.last_attempt_at as marked_suspicious_at,
    NULL::timestamp as probing_started_at,
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
    NULL::text as last_unavailable_reason,
    mps.last_status as last_err_code,
    CASE 
        WHEN mps.next_retry_at <= NOW() THEN 'ready'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '1 minute' THEN '<1min'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '5 minutes' THEN '<5min'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '1 hour' THEN '<1h'
        ELSE '>1h'
    END as retry_in,
    EXTRACT(EPOCH FROM (NOW() - mps.last_attempt_at)) / 60 as state_duration_minutes
FROM model_probe_state mps
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
        WHEN mps.state IN ('failing', 'recovering') THEN 3
        ELSE 4
    END,
    c.id;

-- 2.4 全局探测系统健康度
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

-- 2.5 模型可用性时间线（保留原有定义，如果表存在）
CREATE OR REPLACE VIEW v_model_availability_timeline AS
SELECT 
    mpr.raw_model_name,
    mpr.raw_model_name as outbound_model_name,
    DATE_TRUNC('hour', mpr.created_at) as hour_bucket,
    COUNT(*) as total_probes,
    COUNT(*) FILTER (WHERE mpr.status = 'ok') as successful_probes,
    COUNT(*) FILTER (WHERE mpr.status != 'ok') as failed_probes,
    ROUND((COUNT(*) FILTER (WHERE mpr.status = 'ok') * 100.0 / COUNT(*))::numeric, 2) as success_rate,
    AVG(mpr.latency_ms) FILTER (WHERE mpr.status = 'ok') as avg_latency_ms,
    COUNT(DISTINCT mpr.credential_id) as probed_credentials,
    COUNT(DISTINCT mpr.credential_id) FILTER (WHERE mpr.status = 'ok') as successful_credentials,
    COUNT(DISTINCT mpr.credential_id) FILTER (WHERE mpr.status != 'ok') as failed_credentials
FROM model_probe_runs mpr
WHERE mpr.created_at >= NOW() - INTERVAL '24 hours'
GROUP BY mpr.raw_model_name, DATE_TRUNC('hour', mpr.created_at)
ORDER BY mpr.raw_model_name, hour_bucket DESC;

-- 2.6 辅助函数
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
                WHEN mps.state IN ('failing', 'recovering') THEN 'failing'
                ELSE 'watchdog'
            END as priority
        FROM model_probe_state mps
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

COMMENT ON VIEW v_model_health_dashboard IS '模型健康度总览 (FIXED 2026-06-29)';
COMMENT ON VIEW v_probe_queue_snapshot IS '探测队列快照 (FIXED 2026-06-29)';
COMMENT ON VIEW v_model_priority_details IS '模型优先级详情 (FIXED 2026-06-29)';
COMMENT ON VIEW v_probe_system_health IS '全局探测系统健康度 (FIXED 2026-06-29)';
COMMENT ON VIEW v_model_availability_timeline IS '模型可用性时间线';
COMMENT ON FUNCTION get_model_state_summary(TEXT) IS '获取指定模型的状态分布摘要';
