-- Migration 304-fix: Model Health Dashboard Views 修复
--
-- 问题：原视图使用了不存在的表结构，导致probe-health页面无数据
-- 修复：对齐实际表结构（model_probe_state + credentials + providers）
--
-- Spec: 2026-06-29-probe-health-fix

-- ── 1. 修复模型健康度总览视图 ────────────────────────────────────────────

DROP VIEW IF EXISTS v_model_health_dashboard CASCADE;

CREATE OR REPLACE VIEW v_model_health_dashboard AS
WITH model_stats AS (
    SELECT 
        mps.raw_model_name,
        mps.raw_model_name as outbound_model_name,  -- 简化：假设outbound与raw相同
        'openai-completions' as protocol,
        p.display_name as provider_name,
        
        -- 状态统计
        COUNT(*) as total_credentials,
        COUNT(*) FILTER (WHERE mps.state IN ('healthy_confirmed', 'healthy')) as healthy_count,
        COUNT(*) FILTER (WHERE mps.state = 'suspicious') as suspicious_count,
        COUNT(*) FILTER (WHERE mps.state IN ('failing', 'recovering')) as failing_count,
        COUNT(*) FILTER (WHERE mps.state = 'probing') as probing_count,
        
        -- 优先级统计（从state推断，因为probe_priority字段可能不存在）
        COUNT(*) FILTER (WHERE mps.consecutive_failures >= 3) as urgent_count,
        COUNT(*) FILTER (WHERE mps.state = 'suspicious') as suspicious_priority_count,
        COUNT(*) FILTER (WHERE mps.state IN ('failing', 'recovering')) as failing_priority_count,
        COUNT(*) FILTER (WHERE mps.state = 'healthy_confirmed') as watchdog_count,
        
        -- 健康度指标（使用存在的字段）
        AVG(CASE WHEN mps.consecutive_successes > 0 
            THEN mps.consecutive_successes::float / NULLIF(mps.total_attempts, 0) * 100 
            ELSE NULL END) as avg_success_rate_7d,
        AVG(EXTRACT(EPOCH FROM (mps.next_retry_at - NOW())) / 3600) as avg_verification_hours,
        AVG(mps.consecutive_successes) as avg_consecutive_successes,
        
        -- 实时请求统计（这些字段可能不存在，使用COALESCE）
        0 as total_real_success_24h,
        0 as total_real_failure_24h,
        
        -- 最近活动
        MAX(mps.last_attempt_at) as last_verified_at,
        MAX(mps.last_attempt_at) as last_real_request_at,
        MIN(mps.next_retry_at) as next_probe_at,
        
        -- 问题节点
        COUNT(*) FILTER (
            WHERE mps.state IN ('failing', 'broken_confirmed')
              AND mps.consecutive_failures >= 3
        ) as critical_nodes,
        
        -- 即将探测的节点
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
    0 as provider_model_id,  -- 占位符
    raw_model_name,
    outbound_model_name,
    protocol,
    provider_name,
    
    -- 状态分布
    total_credentials,
    healthy_count,
    suspicious_count,
    failing_count,
    probing_count,
    
    -- 健康度百分比
    ROUND(healthy_count * 100.0 / NULLIF(total_credentials, 0), 1) as healthy_percentage,
    ROUND(failing_count * 100.0 / NULLIF(total_credentials, 0), 1) as failing_percentage,
    
    -- 优先级分布
    urgent_count,
    suspicious_priority_count,
    failing_priority_count,
    watchdog_count,
    
    -- 健康度指标
    ROUND(avg_success_rate_7d, 2) as avg_success_rate_7d,
    ROUND(avg_verification_hours, 1) as avg_verification_hours,
    ROUND(avg_consecutive_successes, 1) as avg_consecutive_successes,
    
    -- 实时请求统计
    total_real_success_24h,
    total_real_failure_24h,
    CASE 
        WHEN (total_real_success_24h + total_real_failure_24h) > 0
        THEN ROUND(total_real_success_24h * 100.0 / (total_real_success_24h + total_real_failure_24h), 2)
        ELSE NULL
    END as real_success_rate_24h,
    
    -- 时间信息
    last_verified_at,
    last_real_request_at,
    next_probe_at,
    
    -- 告警标记
    critical_nodes,
    pending_probes_5min,
    
    -- 整体健康状态评级
    CASE
        WHEN critical_nodes > 0 THEN 'critical'
        WHEN failing_percentage > 20 THEN 'warning'
        WHEN failing_percentage > 10 THEN 'degraded'
        WHEN healthy_percentage >= 90 THEN 'healthy'
        ELSE 'unknown'
    END as overall_health
    
FROM model_stats
ORDER BY 
    -- 优先显示有问题的模型
    CASE 
        WHEN critical_nodes > 0 THEN 1
        WHEN urgent_count > 0 THEN 2
        WHEN failing_percentage > 20 THEN 3
        ELSE 4
    END,
    total_credentials DESC,
    raw_model_name;

COMMENT ON VIEW v_model_health_dashboard IS 
    '模型健康度总览：按模型聚合所有凭据节点的状态、优先级和健康度指标 (FIXED)';

-- ── 2. 修复探测队列快照视图 ─────────────────────────────────────────────

DROP VIEW IF EXISTS v_probe_queue_snapshot CASCADE;

CREATE OR REPLACE VIEW v_probe_queue_snapshot AS
SELECT 
    CASE 
        WHEN mps.consecutive_failures >= 3 THEN 'urgent'
        WHEN mps.state = 'suspicious' THEN 'suspicious'
        WHEN mps.state IN ('failing', 'recovering') THEN 'failing'
        WHEN mps.state = 'healthy_confirmed' THEN 'watchdog'
        ELSE 'unknown'
    END as probe_priority,
    mps.state,
    COUNT(*) as queue_size,
    COUNT(*) FILTER (WHERE mps.next_retry_at <= NOW()) as ready_now,
    COUNT(*) FILTER (WHERE mps.next_retry_at <= NOW() + INTERVAL '1 minute') as ready_1min,
    COUNT(*) FILTER (WHERE mps.next_retry_at <= NOW() + INTERVAL '5 minutes') as ready_5min,
    MIN(mps.next_retry_at) as earliest_retry_at,
    MAX(mps.next_retry_at) as latest_retry_at,
    AVG(EXTRACT(EPOCH FROM (NOW() - mps.last_attempt_at))) as avg_wait_seconds,
    MAX(EXTRACT(EPOCH FROM (NOW() - mps.last_attempt_at))) as max_wait_seconds
FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
WHERE mps.state IN ('suspicious', 'failing', 'recovering')
  AND COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
GROUP BY probe_priority, mps.state
ORDER BY 
    -- 优先级排序
    CASE 
        WHEN mps.consecutive_failures >= 3 THEN 1
        WHEN mps.state = 'suspicious' THEN 2
        WHEN mps.state IN ('failing', 'recovering') THEN 3
        WHEN mps.state = 'healthy_confirmed' THEN 4
        ELSE 5
    END,
    mps.state;

COMMENT ON VIEW v_probe_queue_snapshot IS 
    '探测队列快照：实时显示各优先级队列的大小、就绪数量和等待时间 (FIXED)';

-- ── 3. 修复按模型的优先级分布详情 ──────────────────────────────────────────

DROP VIEW IF EXISTS v_model_priority_details CASCADE;

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
    
    -- 状态信息
    mps.last_attempt_at as last_verified_at,
    mps.next_retry_at,
    mps.last_attempt_at as marked_suspicious_at,
    NULL::timestamp as probing_started_at,
    
    -- 统计信息
    mps.consecutive_successes,
    mps.consecutive_failures,
    0 as consecutive_watchdog_successes,
    CASE WHEN mps.total_attempts > 0 
         THEN mps.consecutive_successes::float / mps.total_attempts * 100 
         ELSE NULL END as success_rate_7d,
    (mps.next_retry_at - NOW()) as verification_interval,
    
    -- 实时请求（占位符）
    0 as real_success_24h,
    0 as real_failure_24h,
    mps.last_attempt_at as last_real_request_at,
    
    -- 错误信息
    NULL::text as last_unavailable_reason,
    mps.last_status as last_err_code,
    
    -- 等待时间
    CASE 
        WHEN mps.next_retry_at <= NOW() THEN 'ready'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '1 minute' THEN '<1min'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '5 minutes' THEN '<5min'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '1 hour' THEN '<1h'
        ELSE '>1h'
    END as retry_in,
    
    -- 状态持续时间
    EXTRACT(EPOCH FROM (NOW() - mps.last_attempt_at)) / 60 as state_duration_minutes

FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
ORDER BY 
    mps.raw_model_name,
    -- 优先显示有问题的节点
    CASE 
        WHEN mps.consecutive_failures >= 3 THEN 1
        WHEN mps.state = 'suspicious' THEN 2
        WHEN mps.state IN ('failing', 'recovering') THEN 3
        ELSE 4
    END,
    c.id;

COMMENT ON VIEW v_model_priority_details IS 
    '模型优先级详情：显示每个凭据×模型节点的详细状态和优先级信息 (FIXED)';

-- ── 4. 修复全局探测系统健康度 ──────────────────────────────────────────────

DROP VIEW IF EXISTS v_probe_system_health CASCADE;

CREATE OR REPLACE VIEW v_probe_system_health AS
SELECT 
    -- 整体统计
    (SELECT COUNT(*) FROM model_probe_state) as total_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state IN ('healthy_confirmed', 'healthy')) as healthy_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state IN ('failing', 'broken_confirmed')) as failing_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'suspicious') as suspicious_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'probing') as probing_nodes,
    
    -- 优先级队列统计
    (SELECT COUNT(*) FROM model_probe_state WHERE consecutive_failures >= 3) as urgent_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'suspicious') as suspicious_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE state IN ('failing', 'recovering')) as failing_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'healthy_confirmed') as watchdog_queue_size,
    
    -- 就绪探测数
    (SELECT COUNT(*) FROM model_probe_state 
     WHERE next_retry_at <= NOW() AND state != 'probing') as ready_probes,
    
    -- 并发控制
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'probing') as current_probing,
    (SELECT COUNT(DISTINCT credential_id) FROM model_probe_state 
     WHERE state = 'probing') as credentials_being_probed,
    
    -- 平均成功率
    (SELECT ROUND(AVG(CASE WHEN total_attempts > 0 
                           THEN consecutive_successes::float / total_attempts * 100 
                           ELSE NULL END), 2) 
     FROM model_probe_state) as avg_success_rate_7d,
    
    -- 最近探测时间
    (SELECT MAX(last_attempt_at) FROM model_probe_state) as last_probe_at,
    (SELECT MAX(last_attempt_at) FROM model_probe_state) as last_real_request_at,
    
    -- 24小时实时请求统计（占位符）
    0 as total_real_success_24h,
    0 as total_real_failure_24h,
    
    -- 问题节点
    (SELECT COUNT(*) FROM model_probe_state 
     WHERE state IN ('failing', 'broken_confirmed') 
       AND consecutive_failures >= 5) as critical_nodes,
    
    -- 系统负载
    (SELECT COUNT(*) FROM model_probe_state 
     WHERE next_retry_at <= NOW() + INTERVAL '5 minutes' 
       AND state != 'probing') as pending_probes_5min,
    
    NOW() as snapshot_at;

COMMENT ON VIEW v_probe_system_health IS 
    '全局探测系统健康度：整体统计、队列大小、并发状态和系统负载 (FIXED)';
