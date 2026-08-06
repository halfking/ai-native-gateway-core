-- V354: 创建会话管理视图 - 支持会话脉络、任务汇总、项目汇总
-- 2026-08-06: 配合 V353 schema 扩展，提供多维度的会话分析视图
--
-- 视图：
--   1. v_session_flow: 会话流程视图（显示同任务下的前后会话关系）
--   2. v_task_summary: 任务汇总视图（按任务聚合会话统计）
--   3. v_project_summary: 项目汇总视图（按项目聚合成本和进度）

-- 1. 会话流程视图：显示会话在任务中的位置和前后关系
DROP VIEW IF EXISTS v_session_flow;
CREATE VIEW v_session_flow AS
SELECT 
  s.session_key,
  s.tenant_id,
  s.gw_project_id,
  s.gw_task_id,
  s.title,
  s.summary,
  s.user_intent,
  s.first_request_at,
  s.last_request_at,
  s.duration_seconds,
  s.request_count,
  s.success_count,
  s.error_count,
  s.total_cost_usd,
  s.total_tokens,
  s.total_prompt_tokens,
  s.total_completion_tokens,
  s.user_tags,
  s.session_status,
  s.models_used,
  s.primary_model,
  -- 同任务下的前后会话（按开始时间排序）
  LAG(s.session_key) OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as prev_session_key,
  LAG(s.title) OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as prev_session_title,
  LEAD(s.session_key) OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as next_session_key,
  LEAD(s.title) OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as next_session_title,
  -- 在任务中的顺序编号
  ROW_NUMBER() OVER (PARTITION BY s.tenant_id, s.gw_task_id ORDER BY s.first_request_at) as session_order_in_task,
  -- 同项目下的前后会话（按开始时间排序）
  LAG(s.session_key) OVER (PARTITION BY s.tenant_id, s.gw_project_id ORDER BY s.first_request_at) as prev_session_in_project,
  LEAD(s.session_key) OVER (PARTITION BY s.tenant_id, s.gw_project_id ORDER BY s.first_request_at) as next_session_in_project
FROM session_summaries s
WHERE s.gw_task_id IS NOT NULL OR s.gw_project_id IS NOT NULL;

COMMENT ON VIEW v_session_flow IS '会话流程视图：显示会话在任务/项目中的前后关系';

-- 2. 任务汇总视图：按任务聚合所有会话的统计信息
-- 注意：不使用 CROSS JOIN LATERAL unnest（空数组会导致整行丢失）。
-- 标签和模型聚合改用简单的 array_cat + array_agg。
DROP VIEW IF EXISTS v_task_summary;
CREATE VIEW v_task_summary AS
SELECT 
  s.tenant_id,
  s.gw_task_id,
  s.gw_project_id,
  COUNT(DISTINCT s.session_key) as session_count,
  MIN(s.first_request_at) as task_started_at,
  MAX(s.last_request_at) as task_last_activity_at,
  EXTRACT(EPOCH FROM (MAX(s.last_request_at) - MIN(s.first_request_at)))::integer as task_duration_seconds,
  SUM(s.request_count) as total_requests,
  SUM(s.success_count) as total_success,
  SUM(s.error_count) as total_errors,
  SUM(s.total_cost_usd) as total_cost_usd,
  SUM(s.total_tokens) as total_tokens,
  SUM(s.total_prompt_tokens) as total_prompt_tokens,
  SUM(s.total_completion_tokens) as total_completion_tokens,
  AVG(s.avg_latency_ms)::integer as avg_latency_ms,
  CASE 
    WHEN BOOL_AND(s.session_status = 'completed') THEN 'completed'
    WHEN BOOL_OR(s.session_status = 'active') THEN 'in_progress'
    ELSE 'abandoned'
  END as task_status,
  ARRAY_AGG(s.title ORDER BY s.first_request_at) FILTER (WHERE s.title IS NOT NULL) as session_titles,
  ARRAY_AGG(s.session_key ORDER BY s.first_request_at) as session_keys
FROM session_summaries s
WHERE s.gw_task_id IS NOT NULL
GROUP BY s.tenant_id, s.gw_task_id, s.gw_project_id;

COMMENT ON VIEW v_task_summary IS '任务汇总视图：按任务聚合会话统计，用于显示任务进度和成本';

-- 3. 项目汇总视图：按项目聚合所有会话和任务的统计信息
DROP VIEW IF EXISTS v_project_summary;
CREATE VIEW v_project_summary AS
SELECT 
  s.tenant_id,
  s.gw_project_id,
  -- 任务和会话数量
  COUNT(DISTINCT s.gw_task_id) as task_count,
  COUNT(DISTINCT s.session_key) as session_count,
  -- 时间范围
  MIN(s.first_request_at) as project_started_at,
  MAX(s.last_request_at) as project_last_activity_at,
  EXTRACT(EPOCH FROM (MAX(s.last_request_at) - MIN(s.first_request_at)))::integer as project_duration_seconds,
  -- 请求统计
  SUM(s.request_count) as total_requests,
  SUM(s.success_count) as total_success,
  SUM(s.error_count) as total_errors,
  -- 成本和 token 统计
  SUM(s.total_cost_usd) as total_cost_usd,
  SUM(s.total_tokens) as total_tokens,
  SUM(s.total_prompt_tokens) as total_prompt_tokens,
  SUM(s.total_completion_tokens) as total_completion_tokens,
  -- 平均延迟
  AVG(s.avg_latency_ms)::integer as avg_latency_ms,
  -- 项目状态推断
  CASE 
    WHEN BOOL_AND(s.session_status = 'completed') THEN 'completed'
    WHEN BOOL_OR(s.session_status = 'active') THEN 'in_progress'
    ELSE 'stalled'
  END as project_status
FROM session_summaries s
WHERE s.gw_project_id IS NOT NULL
GROUP BY s.tenant_id, s.gw_project_id;

COMMENT ON VIEW v_project_summary IS '项目汇总视图：按项目聚合成本和进度，用于项目级别的分析';

-- 4. 创建每日成本统计视图（用于趋势分析）
DROP VIEW IF EXISTS v_daily_session_costs;
CREATE VIEW v_daily_session_costs AS
SELECT 
  s.tenant_id,
  s.gw_project_id,
  s.gw_task_id,
  DATE(s.first_request_at) as date,
  COUNT(DISTINCT s.session_key) as session_count,
  SUM(s.request_count) as total_requests,
  SUM(s.total_cost_usd) as total_cost_usd,
  SUM(s.total_tokens) as total_tokens,
  AVG(s.avg_latency_ms)::integer as avg_latency_ms
FROM session_summaries s
GROUP BY s.tenant_id, s.gw_project_id, s.gw_task_id, DATE(s.first_request_at);

COMMENT ON VIEW v_daily_session_costs IS '每日会话成本视图：按天聚合成本，用于趋势分析';
