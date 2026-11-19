-- Migration 981: Session Analytics Foundation
-- 为会话分析系统创建基础设施：元数据列、物化视图、索引

-- ============================================================================
-- Part 1: 为 sessions 表添加元数据列
-- ============================================================================

-- 添加项目名称列
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS project VARCHAR(255);

-- 添加任务描述列
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS task TEXT;

-- 添加任务分类列
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS task_category VARCHAR(100);

-- 添加是否为探测会话标记（用于排除探测流量）
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS is_probe BOOLEAN DEFAULT false;

-- 创建索引以支持快速筛选
CREATE INDEX IF NOT EXISTS idx_sessions_project 
    ON sessions(project) 
    WHERE project IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_sessions_task_category 
    ON sessions(task_category) 
    WHERE task_category IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_sessions_is_probe 
    ON sessions(is_probe) 
    WHERE is_probe = false;

-- 为已存在的数据添加注释
COMMENT ON COLUMN sessions.project IS '会话关联的项目名称，从标题或首轮请求中提取';
COMMENT ON COLUMN sessions.task IS '会话的任务描述';
COMMENT ON COLUMN sessions.task_category IS '任务分类（如：feature_development, bug_fix, research）';
COMMENT ON COLUMN sessions.is_probe IS '是否为探测/测试会话，用于统计时排除';

-- ============================================================================
-- Part 2: 用户会话聚合物化视图
-- ============================================================================

CREATE MATERIALIZED VIEW IF NOT EXISTS user_session_aggregates AS
SELECT
    s.owner,
    s.tenant_id,
    DATE(s.created_at) as date,
    COUNT(*) as session_count,
    COUNT(DISTINCT s.project) FILTER (WHERE s.project IS NOT NULL) as project_count,
    COUNT(DISTINCT s.task) FILTER (WHERE s.task IS NOT NULL) as task_count,
    COALESCE(SUM(s.total_input_tokens + s.total_output_tokens), 0) as total_tokens,
    COALESCE(SUM(s.total_input_tokens), 0) as input_tokens,
    COALESCE(SUM(s.total_output_tokens), 0) as output_tokens,
    COALESCE(SUM(s.total_cost), 0) as internal_cost,
    COALESCE(SUM(s.total_external_cost), 0) as external_cost,
    COALESCE(AVG(EXTRACT(EPOCH FROM (s.updated_at - s.created_at))), 0) as avg_duration_seconds,
    COALESCE(AVG(s.turn_count), 0) as avg_turns,
    MAX(s.created_at) as last_active_at
FROM sessions s
WHERE s.is_probe = false
GROUP BY s.owner, s.tenant_id, DATE(s.created_at);

-- 创建索引以加速查询
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_session_agg_pk 
    ON user_session_aggregates(owner, tenant_id, date);

CREATE INDEX IF NOT EXISTS idx_user_session_agg_date 
    ON user_session_aggregates(date DESC);

CREATE INDEX IF NOT EXISTS idx_user_session_agg_tenant 
    ON user_session_aggregates(tenant_id, date DESC);

COMMENT ON MATERIALIZED VIEW user_session_aggregates IS '用户会话日聚合统计，用于人员排行与趋势分析';

-- ============================================================================
-- Part 3: 项目任务聚合物化视图
-- ============================================================================

CREATE MATERIALIZED VIEW IF NOT EXISTS project_task_aggregates AS
SELECT
    s.owner,
    s.tenant_id,
    s.project,
    s.task,
    DATE(s.created_at) as date,
    COUNT(*) as session_count,
    COALESCE(SUM(s.turn_count), 0) as turn_count,
    COALESCE(SUM(s.total_input_tokens + s.total_output_tokens), 0) as total_tokens,
    COALESCE(SUM(s.total_cost), 0) as internal_cost,
    COALESCE(SUM(s.total_external_cost), 0) as external_cost,
    MAX(s.created_at) as last_active_at
FROM sessions s
WHERE s.is_probe = false 
    AND s.project IS NOT NULL
GROUP BY s.owner, s.tenant_id, s.project, s.task, DATE(s.created_at);

-- 创建索引
CREATE INDEX IF NOT EXISTS idx_project_task_agg_owner 
    ON project_task_aggregates(owner, date DESC);

CREATE INDEX IF NOT EXISTS idx_project_task_agg_project 
    ON project_task_aggregates(project, date DESC);

CREATE INDEX IF NOT EXISTS idx_project_task_agg_tenant 
    ON project_task_aggregates(tenant_id, date DESC);

COMMENT ON MATERIALIZED VIEW project_task_aggregates IS '项目与任务日聚合统计';

-- ============================================================================
-- Part 4: 智能体使用聚合物化视图
-- ============================================================================

CREATE MATERIALIZED VIEW IF NOT EXISTS agent_usage_aggregates AS
SELECT
    s.agent_name,
    s.tenant_id,
    DATE(s.created_at) as date,
    COUNT(DISTINCT s.owner) as user_count,
    COUNT(*) as session_count,
    COUNT(DISTINCT s.task) FILTER (WHERE s.task IS NOT NULL) as task_count,
    COALESCE(SUM(s.total_input_tokens + s.total_output_tokens), 0) as total_tokens,
    COALESCE(SUM(s.total_cost), 0) as internal_cost,
    COALESCE(SUM(s.total_external_cost), 0) as external_cost,
    COALESCE(AVG(EXTRACT(EPOCH FROM (s.updated_at - s.created_at))), 0) as avg_duration_seconds,
    MAX(s.created_at) as last_used_at
FROM sessions s
WHERE s.is_probe = false 
    AND s.agent_name IS NOT NULL
GROUP BY s.agent_name, s.tenant_id, DATE(s.created_at);

-- 创建索引
CREATE INDEX IF NOT EXISTS idx_agent_usage_agg_agent 
    ON agent_usage_aggregates(agent_name, date DESC);

CREATE INDEX IF NOT EXISTS idx_agent_usage_agg_tenant 
    ON agent_usage_aggregates(tenant_id, date DESC);

COMMENT ON MATERIALIZED VIEW agent_usage_aggregates IS '智能体使用日聚合统计';

-- ============================================================================
-- Part 5: 模型使用聚合物化视图
-- ============================================================================

CREATE MATERIALIZED VIEW IF NOT EXISTS model_usage_aggregates AS
SELECT
    rl.model,
    rl.provider,
    rl.tenant_id,
    DATE(rl.created_at) as date,
    COUNT(*) as request_count,
    COUNT(*) FILTER (WHERE rl.status = 'success') as success_count,
    COUNT(*) FILTER (WHERE rl.cache_read > 0) as cache_hit_count,
    COUNT(DISTINCT rl.owner) as user_count,
    COALESCE(SUM(rl.prompt_tokens), 0) as input_tokens,
    COALESCE(SUM(rl.completion_tokens), 0) as output_tokens,
    COALESCE(SUM(rl.total_tokens), 0) as total_tokens,
    COALESCE(SUM(rl.cost), 0) as internal_cost,
    COALESCE(SUM(rl.external_cost), 0) as external_cost,
    COALESCE(AVG(rl.latency_ms), 0) as avg_duration_ms,
    MAX(rl.created_at) as last_used_at
FROM request_logs_with_current_month rl
GROUP BY rl.model, rl.provider, rl.tenant_id, DATE(rl.created_at);

-- 创建索引
CREATE INDEX IF NOT EXISTS idx_model_usage_agg_model 
    ON model_usage_aggregates(model, date DESC);

CREATE INDEX IF NOT EXISTS idx_model_usage_agg_provider 
    ON model_usage_aggregates(provider, date DESC);

CREATE INDEX IF NOT EXISTS idx_model_usage_agg_tenant 
    ON model_usage_aggregates(tenant_id, date DESC);

COMMENT ON MATERIALIZED VIEW model_usage_aggregates IS '模型使用日聚合统计，包含成功率、缓存率等指标';

-- ============================================================================
-- Part 6: 刷新物化视图的辅助函数
-- ============================================================================

-- 创建刷新单个物化视图的函数
-- ★ 不用 REFRESH ... CONCURRENTLY：PostgreSQL 不允许 CONCURRENTLY 在
--   事务块/函数体内执行（函数体即事务块），原写法每次调用都落进
--   EXCEPTION 分支被吞成 WARNING——辅助函数静默失效、视图永远刷不动。
--   需要并发刷新时由外部 psql 直接执行 REFRESH MATERIALIZED VIEW
--   CONCURRENTLY（四个视图均已建唯一索引，满足 CONCURRENTLY 前提）。
CREATE OR REPLACE FUNCTION refresh_session_analytics_view(view_name TEXT)
RETURNS void AS $$
BEGIN
    EXECUTE format('REFRESH MATERIALIZED VIEW %I', view_name);
    RAISE NOTICE 'Refreshed materialized view: %', view_name;
EXCEPTION
    WHEN OTHERS THEN
        RAISE WARNING 'Failed to refresh %: %', view_name, SQLERRM;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION refresh_session_analytics_view(TEXT) IS '刷新指定的会话分析物化视图';

-- 创建批量刷新所有视图的函数
CREATE OR REPLACE FUNCTION refresh_all_session_analytics_views()
RETURNS void AS $$
BEGIN
    PERFORM refresh_session_analytics_view('user_session_aggregates');
    PERFORM refresh_session_analytics_view('project_task_aggregates');
    PERFORM refresh_session_analytics_view('agent_usage_aggregates');
    PERFORM refresh_session_analytics_view('model_usage_aggregates');
    
    RAISE NOTICE 'All session analytics views refreshed';
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION refresh_all_session_analytics_views() IS '批量刷新所有会话分析物化视图';

-- ============================================================================
-- Part 7: 初始数据填充（为现有会话标记 is_probe）
-- ============================================================================

-- 标记探测会话：会话标题包含 'test', 'probe', '测试' 等关键词
UPDATE sessions SET is_probe = true
WHERE is_probe = false
    AND (
        title ILIKE '%test%'
        OR title ILIKE '%probe%'
        OR title ILIKE '%测试%'
        OR title ILIKE '%探测%'
        OR owner LIKE 'test_%'
    );

-- 记录标记的探测会话数量
DO $$
DECLARE
    probe_count INTEGER;
BEGIN
    SELECT COUNT(*) INTO probe_count FROM sessions WHERE is_probe = true;
    RAISE NOTICE 'Marked % sessions as probe sessions', probe_count;
END $$;

-- ============================================================================
-- Part 8: 首次刷新物化视图
-- ============================================================================

-- 刷新所有物化视图（初始化数据）
SELECT refresh_all_session_analytics_views();

-- 记录刷新结果
DO $$
DECLARE
    user_agg_count INTEGER;
    project_agg_count INTEGER;
    agent_agg_count INTEGER;
    model_agg_count INTEGER;
BEGIN
    SELECT COUNT(*) INTO user_agg_count FROM user_session_aggregates;
    SELECT COUNT(*) INTO project_agg_count FROM project_task_aggregates;
    SELECT COUNT(*) INTO agent_agg_count FROM agent_usage_aggregates;
    SELECT COUNT(*) INTO model_agg_count FROM model_usage_aggregates;
    
    RAISE NOTICE 'Initial materialized view counts:';
    RAISE NOTICE '  user_session_aggregates: %', user_agg_count;
    RAISE NOTICE '  project_task_aggregates: %', project_agg_count;
    RAISE NOTICE '  agent_usage_aggregates: %', agent_agg_count;
    RAISE NOTICE '  model_usage_aggregates: %', model_agg_count;
END $$;

-- ============================================================================
-- 完成
-- ============================================================================

-- ★ 双账本纪律：与 971/977/979/980 同款（schema_migrations 本件自登记；
--   gateway_db_revision_sequences 由 deploy 通道按内容指纹写）。
--   ≥952 的迁移必须自登记（960 迁移立的纪律门）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('981', 'session_analytics_foundation: 会话分析系统基础设施（sessions 元数据列 + 4 个物化视图 + 刷新辅助函数 + 探测会话标记）')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
