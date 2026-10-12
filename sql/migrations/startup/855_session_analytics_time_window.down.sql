-- Migration 855 (down): 撤掉 session-analytics 三兄弟的 30 天时间窗
--
-- 回到 357 的无窗口口径（全历史聚合）。
-- 撤销后视图会重新膨胀到约 104,299 个 task 组、182 MB 堆+索引，
-- 每轮重写 WAL 回到约 169 MB —— 这正是 855 要去掉的成本。
-- ⚠️ 回滚会把 30 天窗口之后仍在活动的老 task 组重新纳入统计，
--    报表数值会变（通常是变大），属口径回退而非数据丢失。

BEGIN;

DROP MATERIALIZED VIEW IF EXISTS session_client_stats;

CREATE MATERIALIZED VIEW session_client_stats AS
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

CREATE UNIQUE INDEX IF NOT EXISTS idx_session_client_stats_tenant_client
    ON session_client_stats(tenant_id, client_id);
CREATE INDEX IF NOT EXISTS idx_session_client_stats_cost
    ON session_client_stats(tenant_id, total_cost_usd DESC);
CREATE INDEX IF NOT EXISTS idx_session_client_stats_sessions
    ON session_client_stats(tenant_id, session_count DESC);
CREATE INDEX IF NOT EXISTS idx_session_client_stats_health
    ON session_client_stats(tenant_id, avg_health_score DESC NULLS LAST);

DROP MATERIALIZED VIEW IF EXISTS session_task_stats;

CREATE MATERIALIZED VIEW session_task_stats AS
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

CREATE UNIQUE INDEX IF NOT EXISTS idx_session_task_stats_tenant_task
    ON session_task_stats(tenant_id, task_id);
CREATE INDEX IF NOT EXISTS idx_session_task_stats_cost
    ON session_task_stats(tenant_id, total_cost_usd DESC);
CREATE INDEX IF NOT EXISTS idx_session_task_stats_sessions
    ON session_task_stats(tenant_id, session_count DESC);
CREATE INDEX IF NOT EXISTS idx_session_task_stats_health
    ON session_task_stats(tenant_id, avg_health_score DESC NULLS LAST);

DROP MATERIALIZED VIEW IF EXISTS session_client_task_matrix;

CREATE MATERIALIZED VIEW session_client_task_matrix AS
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

CREATE UNIQUE INDEX IF NOT EXISTS idx_session_client_task_matrix_uq
    ON session_client_task_matrix(tenant_id, client_id, task_id);
CREATE INDEX IF NOT EXISTS idx_session_client_task_matrix_client
    ON session_client_task_matrix(tenant_id, client_id, total_cost_usd DESC);
CREATE INDEX IF NOT EXISTS idx_session_client_task_matrix_task
    ON session_client_task_matrix(tenant_id, task_id, total_cost_usd DESC);

COMMENT ON MATERIALIZED VIEW session_client_stats IS
'客户端维度会话聚合统计（物化视图，855 已回滚：恢复全历史口径）';
COMMENT ON MATERIALIZED VIEW session_task_stats IS
'任务维度会话聚合统计（物化视图，855 已回滚：恢复全历史口径）';
COMMENT ON MATERIALIZED VIEW session_client_task_matrix IS
'客户端-任务关联矩阵（物化视图，855 已回滚：恢复全历史口径）';

COMMIT;