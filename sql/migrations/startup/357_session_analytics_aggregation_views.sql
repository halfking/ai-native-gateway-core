-- Migration 357: Session Analytics Aggregation Views
-- 2026-07-07: 为会话分析创建客户端和任务维度的聚合物化视图
-- P2阶段：支持客户端/任务详细分析页面
-- Ref: P1阶段交接文档，客户端/任务维度分析需求

BEGIN;

-- ============================================================
-- 1. 客户端维度聚合视图
-- ============================================================
-- 目的：按客户端ID聚合会话统计，支持 Top N 排行和详情页查询
-- 数据源：session_summaries.client_models[1] 作为客户端标识
CREATE MATERIALIZED VIEW IF NOT EXISTS session_client_stats AS
SELECT 
    tenant_id,
    COALESCE(client_models[1], 'unknown') as client_id,
    COUNT(*) as session_count,
    COUNT(*) FILTER (WHERE last_request_at >= NOW() - INTERVAL '24 hours') as active_sessions_24h,
    SUM(request_count) as total_requests,
    SUM(total_cost_usd) as total_cost_usd,
    AVG(total_cost_usd) as avg_cost_per_session,
    AVG(health_score)::INT as avg_health_score,
    COUNT(*) FILTER (WHERE health_grade = 'A') as health_grade_a_count,
    COUNT(*) FILTER (WHERE health_grade = 'B') as health_grade_b_count,
    COUNT(*) FILTER (WHERE health_grade = 'C') as health_grade_c_count,
    COUNT(*) FILTER (WHERE health_grade = 'D') as health_grade_d_count,
    COUNT(*) FILTER (WHERE health_grade = 'F') as health_grade_f_count,
    SUM(success_count) as total_success,
    SUM(error_count) as total_errors,
    AVG(avg_latency_ms)::INT as avg_latency_ms,
    MIN(first_request_at) as first_seen_at,
    MAX(last_request_at) as last_seen_at,
    array_agg(DISTINCT primary_model) FILTER (WHERE primary_model IS NOT NULL) as models_used,
    NOW() as refreshed_at
FROM session_summaries
GROUP BY tenant_id, client_models[1];

-- 创建索引
CREATE UNIQUE INDEX idx_session_client_stats_tenant_client 
    ON session_client_stats(tenant_id, client_id);
CREATE INDEX idx_session_client_stats_cost 
    ON session_client_stats(tenant_id, total_cost_usd DESC);
CREATE INDEX idx_session_client_stats_sessions 
    ON session_client_stats(tenant_id, session_count DESC);
CREATE INDEX idx_session_client_stats_health 
    ON session_client_stats(tenant_id, avg_health_score DESC NULLS LAST);

COMMENT ON MATERIALIZED VIEW session_client_stats IS 
'客户端维度会话聚合统计（物化视图，需定期刷新）';

-- ============================================================
-- 2. 任务维度聚合视图
-- ============================================================
-- 目的：按任务ID聚合会话统计，支持 Top N 排行和详情页查询
-- 数据源：session_dim.task_id 关联 session_summaries
CREATE MATERIALIZED VIEW IF NOT EXISTS session_task_stats AS
SELECT 
    ss.tenant_id,
    COALESCE(sd.task_id, 'unknown') as task_id,
    COUNT(*) as session_count,
    COUNT(*) FILTER (WHERE ss.last_request_at >= NOW() - INTERVAL '24 hours') as active_sessions_24h,
    SUM(ss.request_count) as total_requests,
    SUM(ss.total_cost_usd) as total_cost_usd,
    AVG(ss.total_cost_usd) as avg_cost_per_session,
    AVG(ss.health_score)::INT as avg_health_score,
    COUNT(*) FILTER (WHERE ss.health_grade = 'A') as health_grade_a_count,
    COUNT(*) FILTER (WHERE ss.health_grade = 'B') as health_grade_b_count,
    COUNT(*) FILTER (WHERE ss.health_grade = 'C') as health_grade_c_count,
    COUNT(*) FILTER (WHERE ss.health_grade = 'D') as health_grade_d_count,
    COUNT(*) FILTER (WHERE ss.health_grade = 'F') as health_grade_f_count,
    SUM(ss.success_count) as total_success,
    SUM(ss.error_count) as total_errors,
    AVG(ss.avg_latency_ms)::INT as avg_latency_ms,
    MIN(ss.first_request_at) as first_seen_at,
    MAX(ss.last_request_at) as last_seen_at,
    array_agg(DISTINCT ss.primary_model) FILTER (WHERE ss.primary_model IS NOT NULL) as models_used,
    array_agg(DISTINCT ss.client_models[1]) FILTER (WHERE ss.client_models[1] IS NOT NULL) as clients_used,
    NOW() as refreshed_at
FROM session_summaries ss
LEFT JOIN session_dim sd ON ss.session_key = sd.gw_session_id AND ss.tenant_id = sd.tenant_id
GROUP BY ss.tenant_id, sd.task_id;

-- 创建索引
CREATE UNIQUE INDEX idx_session_task_stats_tenant_task 
    ON session_task_stats(tenant_id, task_id);
CREATE INDEX idx_session_task_stats_cost 
    ON session_task_stats(tenant_id, total_cost_usd DESC);
CREATE INDEX idx_session_task_stats_sessions 
    ON session_task_stats(tenant_id, session_count DESC);
CREATE INDEX idx_session_task_stats_health 
    ON session_task_stats(tenant_id, avg_health_score DESC NULLS LAST);

COMMENT ON MATERIALIZED VIEW session_task_stats IS 
'任务维度会话聚合统计（物化视图，需定期刷新）';

-- ============================================================
-- 3. 客户端-任务关联视图
-- ============================================================
-- 目的：分析客户端与任务的关联关系（哪些客户端在哪些任务上消费最多）
CREATE MATERIALIZED VIEW IF NOT EXISTS session_client_task_matrix AS
SELECT 
    ss.tenant_id,
    COALESCE(ss.client_models[1], 'unknown') as client_id,
    COALESCE(sd.task_id, 'unknown') as task_id,
    COUNT(*) as session_count,
    SUM(ss.total_cost_usd) as total_cost_usd,
    AVG(ss.health_score)::INT as avg_health_score,
    MAX(ss.last_request_at) as last_activity_at,
    NOW() as refreshed_at
FROM session_summaries ss
LEFT JOIN session_dim sd ON ss.session_key = sd.gw_session_id AND ss.tenant_id = sd.tenant_id
GROUP BY ss.tenant_id, ss.client_models[1], sd.task_id;

-- 唯一索引：REFRESH MATERIALIZED VIEW CONCURRENTLY 要求物化视图有唯一索引
CREATE UNIQUE INDEX idx_session_client_task_matrix_uq
    ON session_client_task_matrix(tenant_id, client_id, task_id);
CREATE INDEX idx_session_client_task_matrix_client 
    ON session_client_task_matrix(tenant_id, client_id, total_cost_usd DESC);
CREATE INDEX idx_session_client_task_matrix_task 
    ON session_client_task_matrix(tenant_id, task_id, total_cost_usd DESC);

COMMENT ON MATERIALIZED VIEW session_client_task_matrix IS 
'客户端-任务关联矩阵（用于交叉分析）';

-- ============================================================
-- 4. 刷新函数（手动触发或定时任务调用）
-- ============================================================
CREATE OR REPLACE FUNCTION refresh_session_analytics_views()
RETURNS void AS $$
BEGIN
    REFRESH MATERIALIZED VIEW CONCURRENTLY session_client_stats;
    REFRESH MATERIALIZED VIEW CONCURRENTLY session_task_stats;
    REFRESH MATERIALIZED VIEW CONCURRENTLY session_client_task_matrix;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION refresh_session_analytics_views() IS 
'刷新会话分析物化视图（建议每小时或每日执行）';

-- ============================================================
-- 5. 初始刷新（首次创建后立即填充数据）
-- ============================================================
SELECT refresh_session_analytics_views();

COMMIT;
