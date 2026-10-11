-- Migration 855: 给 session-analytics 三兄弟加 30 天时间窗
--
-- 背景（docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md
-- §10.106.37 / §10.106.39）：
--   · 三兄弟每轮重写约 169.3 MB WAL，占集群 WAL 的 62%~75%（实测差分）
--   · 它们没有时间窗，每轮全扫 session_summaries（682,049 行 / 317 MB）
--   · 代价的驱动量是**输出组数**：task 维度有 104,299 个长尾历史 task_id
--
-- 实测收益（154 生产，2026-10-12）：
--   输出组数   104,299 → 1,832   （-98.2%）
--   CPU        28.7s → 26.4s     （-8%，省不掉的是全表顺序扫那一段）
-- ⇒ 这是一刀砍在 WAL 上、而不是 CPU 上的改动。
--
-- ⚠️ 口径变更（owner 已知悉并拍板）：报表不再包含 30 天以前的会话。
-- 数据最早只到 2026-08-06，所以 30 天窗意味着**八月整月从报表里消失**。
--
-- 为什么不用「状态表 + 读状态表的截止时间」那套（§10.106.34 的写法）：
--   物化视图的定义里**本来就可以出现 NOW()**——357 就是这么建的（目标列里
--   就有 `NOW() as refreshed_at`），PG 只禁止它出现在索引表达式与生成列里。
--   所以直接写 `WHERE ... >= NOW() - INTERVAL '30 days'` 即可，
--   不必新增状态表，也就不必改 admin 侧那 4 个 refreshed_at 读点。
--
-- ⚠️ 关于「去掉 NOW() 列能降低写放大」的说明：§10.106.39 用
--   routing_analytics_7d（837 已去掉 NOW()）的 n_tup_del ≈ n_tup_ins 实测否掉了
--   那个前提——REFRESH CONCURRENTLY 本身就是整份快照替换，与列里有没有 NOW()
--   无关。**所以本迁移保留 refreshed_at 列**：它承载 4 个 admin 读点，
--   而留着它并不额外付出写放大。
--
-- 改窗口长度：改下面三处的 INTERVAL 字面量，再发一个迁移重建视图。
-- （视图定义里的字面量是 MV 的定义的一部分，不能靠 UPDATE 改。）

BEGIN;

-- 30 天窗口：session 只要在窗口内有请求就计入（用 last_request_at，
-- 不用 created_at —— 一条三月创建、四月还在用的会话应该被计入）。
-- 注：active_sessions_24h 那个 FILTER 不受影响 —— 24 小时内的会话必然
-- 落在 30 天窗口内。

-- ============================================================
-- 1. 客户端维度聚合视图
-- ============================================================
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
WHERE last_request_at >= NOW() - INTERVAL '30 days'
GROUP BY tenant_id, client_models[1];

CREATE UNIQUE INDEX IF NOT EXISTS idx_session_client_stats_tenant_client
    ON session_client_stats(tenant_id, client_id);
CREATE INDEX IF NOT EXISTS idx_session_client_stats_cost
    ON session_client_stats(tenant_id, total_cost_usd DESC);
CREATE INDEX IF NOT EXISTS idx_session_client_stats_sessions
    ON session_client_stats(tenant_id, session_count DESC);
CREATE INDEX IF NOT EXISTS idx_session_client_stats_health
    ON session_client_stats(tenant_id, avg_health_score DESC NULLS LAST);

COMMENT ON MATERIALIZED VIEW session_client_stats IS
'客户端维度会话聚合统计（物化视图，仅统计近 30 天；迁移 855 加时间窗）';

-- ============================================================
-- 2. 任务维度聚合视图
-- ============================================================
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
WHERE ss.last_request_at >= NOW() - INTERVAL '30 days'
GROUP BY ss.tenant_id, sd.task_id;

CREATE UNIQUE INDEX IF NOT EXISTS idx_session_task_stats_tenant_task
    ON session_task_stats(tenant_id, task_id);
CREATE INDEX IF NOT EXISTS idx_session_task_stats_cost
    ON session_task_stats(tenant_id, total_cost_usd DESC);
CREATE INDEX IF NOT EXISTS idx_session_task_stats_sessions
    ON session_task_stats(tenant_id, session_count DESC);
CREATE INDEX IF NOT EXISTS idx_session_task_stats_health
    ON session_task_stats(tenant_id, avg_health_score DESC NULLS LAST);

COMMENT ON MATERIALIZED VIEW session_task_stats IS
'任务维度会话聚合统计（物化视图，仅统计近 30 天；迁移 855 加时间窗）';

-- ============================================================
-- 3. 客户端-任务关联视图
-- ============================================================
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
WHERE ss.last_request_at >= NOW() - INTERVAL '30 days'
GROUP BY ss.tenant_id, ss.client_models[1], sd.task_id;

CREATE UNIQUE INDEX IF NOT EXISTS idx_session_client_task_matrix_uq
    ON session_client_task_matrix(tenant_id, client_id, task_id);
CREATE INDEX IF NOT EXISTS idx_session_client_task_matrix_client
    ON session_client_task_matrix(tenant_id, client_id, total_cost_usd DESC);
CREATE INDEX IF NOT EXISTS idx_session_client_task_matrix_task
    ON session_client_task_matrix(tenant_id, task_id, total_cost_usd DESC);

COMMENT ON MATERIALIZED VIEW session_client_task_matrix IS
'客户端-任务关联矩阵（物化视图，仅统计近 30 天；迁移 855 加时间窗）';

COMMIT;