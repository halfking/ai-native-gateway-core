-- Migration 303: Model Health Dashboard Views
--
-- Why: 需要一个监控总览，能够：
--   1. 按模型查看所有凭据节点的状态分布
--   2. 查看当前探测队列的优先级和任务数
--   3. 实时了解系统健康度
--
-- 核心视图：
--   v_model_health_dashboard: 按模型聚合所有节点状态
--   v_probe_queue_snapshot:   实时探测队列快照
--   v_model_priority_summary: 按模型统计优先级分布
--
-- Spec: 2026-06-28-model-health-dashboard

-- ── 1. 模型健康度总览视图 ────────────────────────────────────────────

CREATE OR REPLACE VIEW v_model_health_dashboard AS
WITH model_stats AS (
    SELECT 
        pm.id as provider_model_id,
        pm.raw_model_name,
        pm.outbound_model_name,
        pm.protocol,
        p.name as provider_name,
        
        -- 状态统计
        COUNT(*) as total_credentials,
        COUNT(*) FILTER (WHERE mps.state = 'healthy') as healthy_count,
        COUNT(*) FILTER (WHERE mps.state = 'suspicious') as suspicious_count,
        COUNT(*) FILTER (WHERE mps.state = 'failing') as failing_count,
        COUNT(*) FILTER (WHERE mps.state = 'probing') as probing_count,
        
        -- 优先级统计
        COUNT(*) FILTER (WHERE mps.probe_priority = 'urgent') as urgent_count,
        COUNT(*) FILTER (WHERE mps.probe_priority = 'suspicious') as suspicious_priority_count,
        COUNT(*) FILTER (WHERE mps.probe_priority = 'failing') as failing_priority_count,
        COUNT(*) FILTER (WHERE mps.probe_priority = 'watchdog') as watchdog_count,
        
        -- 健康度指标
        AVG(mps.success_rate_7d) as avg_success_rate_7d,
        AVG(EXTRACT(EPOCH FROM mps.verification_interval) / 3600) as avg_verification_hours,
        AVG(mps.consecutive_watchdog_successes) as avg_consecutive_successes,
        
        -- 实时请求统计（24小时）
        SUM(mps.real_request_success_count) as total_real_success_24h,
        SUM(mps.real_request_failure_count) as total_real_failure_24h,
        
        -- 最近活动
        MAX(mps.last_verified_at) as last_verified_at,
        MAX(mps.last_real_request_at) as last_real_request_at,
        MIN(mps.next_retry_at) as next_probe_at,
        
        -- 问题节点
        COUNT(*) FILTER (
            WHERE mps.state = 'failing' 
              AND mps.consecutive_failures >= 3
        ) as critical_nodes,
        
        -- 即将探测的节点
        COUNT(*) FILTER (
            WHERE mps.next_retry_at <= NOW() + INTERVAL '5 minutes'
              AND mps.state != 'probing'
        ) as pending_probes_5min
        
    FROM provider_models pm
    LEFT JOIN credential_model_bindings cmb ON cmb.provider_model_id = pm.id
    LEFT JOIN model_probe_state mps ON mps.credential_id = cmb.credential_id 
        AND mps.raw_model_name = pm.raw_model_name
    LEFT JOIN credentials c ON c.id = cmb.credential_id
    LEFT JOIN providers p ON p.id = pm.provider_id
    WHERE COALESCE(c.status, 'active') = 'active'
      AND COALESCE(c.lifecycle_status, 'active') = 'active'
      AND COALESCE(c.manual_disabled, FALSE) = FALSE
    GROUP BY pm.id, pm.raw_model_name, pm.outbound_model_name, pm.protocol, p.name
)
SELECT 
    provider_model_id,
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
    '模型健康度总览：按模型聚合所有凭据节点的状态、优先级和健康度指标';

-- ── 2. 探测队列快照视图 ─────────────────────────────────────────────

CREATE OR REPLACE VIEW v_probe_queue_snapshot AS
SELECT 
    mps.probe_priority,
    mps.state,
    COUNT(*) as queue_size,
    COUNT(*) FILTER (WHERE mps.next_retry_at <= NOW()) as ready_now,
    COUNT(*) FILTER (WHERE mps.next_retry_at <= NOW() + INTERVAL '1 minute') as ready_1min,
    COUNT(*) FILTER (WHERE mps.next_retry_at <= NOW() + INTERVAL '5 minutes') as ready_5min,
    MIN(mps.next_retry_at) as earliest_retry_at,
    MAX(mps.next_retry_at) as latest_retry_at,
    AVG(EXTRACT(EPOCH FROM (NOW() - mps.marked_suspicious_at))) as avg_wait_seconds,
    MAX(EXTRACT(EPOCH FROM (NOW() - mps.marked_suspicious_at))) as max_wait_seconds
FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
WHERE mps.state IN ('suspicious', 'failing')
  AND COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
GROUP BY mps.probe_priority, mps.state
ORDER BY 
    -- 优先级排序
    CASE mps.probe_priority
        WHEN 'urgent' THEN 1
        WHEN 'suspicious' THEN 2
        WHEN 'failing' THEN 3
        WHEN 'recovering' THEN 4
        WHEN 'watchdog' THEN 5
        ELSE 6
    END,
    mps.state;

COMMENT ON VIEW v_probe_queue_snapshot IS 
    '探测队列快照：实时显示各优先级队列的大小、就绪数量和等待时间';

-- ── 3. 按模型的优先级分布详情 ──────────────────────────────────────────

CREATE OR REPLACE VIEW v_model_priority_details AS
SELECT 
    pm.raw_model_name,
    pm.outbound_model_name,
    mps.probe_priority,
    mps.state,
    c.id as credential_id,
    c.label as credential_label,
    p.name as provider_name,
    
    -- 状态信息
    mps.last_verified_at,
    mps.next_retry_at,
    mps.marked_suspicious_at,
    mps.probing_started_at,
    
    -- 统计信息
    mps.consecutive_successes,
    mps.consecutive_failures,
    mps.consecutive_watchdog_successes,
    mps.success_rate_7d,
    mps.verification_interval,
    
    -- 实时请求
    mps.real_request_success_count as real_success_24h,
    mps.real_request_failure_count as real_failure_24h,
    mps.last_real_request_at,
    
    -- 错误信息
    mps.last_unavailable_reason,
    mps.last_err_code,
    
    -- 等待时间
    CASE 
        WHEN mps.next_retry_at <= NOW() THEN 'ready'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '1 minute' THEN '<1min'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '5 minutes' THEN '<5min'
        WHEN mps.next_retry_at <= NOW() + INTERVAL '1 hour' THEN '<1h'
        ELSE '>1h'
    END as retry_in,
    
    -- 状态持续时间
    EXTRACT(EPOCH FROM (NOW() - COALESCE(mps.last_attempt_at, mps.created_at))) / 60 as state_duration_minutes

FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
JOIN providers p ON p.id = c.provider_id
WHERE COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
ORDER BY 
    pm.raw_model_name,
    -- 优先显示有问题的节点
    CASE mps.probe_priority
        WHEN 'urgent' THEN 1
        WHEN 'suspicious' THEN 2
        WHEN 'failing' THEN 3
        ELSE 4
    END,
    CASE mps.state
        WHEN 'failing' THEN 1
        WHEN 'suspicious' THEN 2
        WHEN 'probing' THEN 3
        ELSE 4
    END,
    c.id;

COMMENT ON VIEW v_model_priority_details IS 
    '模型优先级详情：显示每个凭据×模型节点的详细状态和优先级信息';

-- ── 4. 全局探测系统健康度 ──────────────────────────────────────────────

CREATE OR REPLACE VIEW v_probe_system_health AS
SELECT 
    -- 整体统计
    (SELECT COUNT(*) FROM model_probe_state) as total_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'healthy') as healthy_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'failing') as failing_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'suspicious') as suspicious_nodes,
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'probing') as probing_nodes,
    
    -- 优先级队列统计
    (SELECT COUNT(*) FROM model_probe_state WHERE probe_priority = 'urgent') as urgent_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE probe_priority = 'suspicious') as suspicious_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE probe_priority = 'failing') as failing_queue_size,
    (SELECT COUNT(*) FROM model_probe_state WHERE probe_priority = 'watchdog') as watchdog_queue_size,
    
    -- 就绪探测数
    (SELECT COUNT(*) FROM model_probe_state 
     WHERE next_retry_at <= NOW() AND state != 'probing') as ready_probes,
    
    -- 并发控制
    (SELECT COUNT(*) FROM model_probe_state WHERE state = 'probing') as current_probing,
    (SELECT COUNT(DISTINCT credential_id) FROM model_probe_state 
     WHERE state = 'probing') as credentials_being_probed,
    
    -- 平均成功率
    (SELECT ROUND(AVG(success_rate_7d), 2) FROM model_probe_state 
     WHERE success_rate_7d IS NOT NULL) as avg_success_rate_7d,
    
    -- 最近探测时间
    (SELECT MAX(last_verified_at) FROM model_probe_state) as last_probe_at,
    (SELECT MAX(last_real_request_at) FROM model_probe_state) as last_real_request_at,
    
    -- 24小时实时请求统计
    (SELECT SUM(real_request_success_count) FROM model_probe_state) as total_real_success_24h,
    (SELECT SUM(real_request_failure_count) FROM model_probe_state) as total_real_failure_24h,
    
    -- 问题节点
    (SELECT COUNT(*) FROM model_probe_state 
     WHERE state = 'failing' AND consecutive_failures >= 5) as critical_nodes,
    
    -- 系统负载
    (SELECT COUNT(*) FROM model_probe_state 
     WHERE next_retry_at <= NOW() + INTERVAL '5 minutes' 
       AND state != 'probing') as pending_probes_5min,
    
    NOW() as snapshot_at;

COMMENT ON VIEW v_probe_system_health IS 
    '全局探测系统健康度：整体统计、队列大小、并发状态和系统负载';

-- ── 5. 模型可用性时间线（最近24小时）────────────────────────────────

CREATE OR REPLACE VIEW v_model_availability_timeline AS
SELECT 
    pm.raw_model_name,
    pm.outbound_model_name,
    DATE_TRUNC('hour', mpr.created_at) as hour_bucket,
    
    -- 探测统计
    COUNT(*) as total_probes,
    COUNT(*) FILTER (WHERE mpr.status = 'ok') as successful_probes,
    COUNT(*) FILTER (WHERE mpr.status != 'ok') as failed_probes,
    ROUND(COUNT(*) FILTER (WHERE mpr.status = 'ok') * 100.0 / COUNT(*), 2) as success_rate,
    
    -- 平均延迟
    AVG(mpr.latency_ms) FILTER (WHERE mpr.status = 'ok') as avg_latency_ms,
    
    -- 状态分布
    COUNT(DISTINCT mpr.credential_id) as probed_credentials,
    
    -- 唯一凭据的成功/失败
    COUNT(DISTINCT mpr.credential_id) FILTER (WHERE mpr.status = 'ok') as successful_credentials,
    COUNT(DISTINCT mpr.credential_id) FILTER (WHERE mpr.status != 'ok') as failed_credentials

FROM model_probe_runs mpr
JOIN provider_models pm ON pm.raw_model_name = mpr.raw_model_name
WHERE mpr.created_at >= NOW() - INTERVAL '24 hours'
GROUP BY pm.raw_model_name, pm.outbound_model_name, DATE_TRUNC('hour', mpr.created_at)
ORDER BY pm.raw_model_name, hour_bucket DESC;

COMMENT ON VIEW v_model_availability_timeline IS 
    '模型可用性时间线：最近24小时按小时聚合的探测成功率和延迟';

-- ── 6. 创建辅助函数：获取模型的状态分布摘要 ────────────────────────

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
        mps.state::TEXT,
        mps.probe_priority::TEXT,
        COUNT(*) as count,
        ROUND(AVG(mps.success_rate_7d), 2) as avg_success_rate,
        EXTRACT(EPOCH FROM MIN(mps.next_retry_at - NOW()))::INTEGER as next_probe_in_seconds
    FROM model_probe_state mps
    JOIN credentials c ON c.id = mps.credential_id
    WHERE mps.raw_model_name = p_raw_model_name
      AND COALESCE(c.status, 'active') = 'active'
      AND COALESCE(c.lifecycle_status, 'active') = 'active'
      AND COALESCE(c.manual_disabled, FALSE) = FALSE
    GROUP BY mps.state, mps.probe_priority
    ORDER BY 
        CASE mps.probe_priority
            WHEN 'urgent' THEN 1
            WHEN 'suspicious' THEN 2
            WHEN 'failing' THEN 3
            WHEN 'recovering' THEN 4
            WHEN 'watchdog' THEN 5
        END,
        CASE mps.state
            WHEN 'failing' THEN 1
            WHEN 'suspicious' THEN 2
            WHEN 'probing' THEN 3
            WHEN 'healthy' THEN 4
        END;
$$;

COMMENT ON FUNCTION get_model_state_summary(TEXT) IS 
    '获取指定模型的状态分布摘要，包括每个状态×优先级组合的节点数和平均成功率';
