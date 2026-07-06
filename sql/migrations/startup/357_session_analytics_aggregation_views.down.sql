-- Migration 357: Rollback Session Analytics Aggregation Views
-- 2026-07-07: 回滚客户端和任务维度的聚合物化视图

BEGIN;

-- 删除刷新函数
DROP FUNCTION IF EXISTS refresh_session_analytics_views();

-- 删除物化视图
DROP MATERIALIZED VIEW IF EXISTS session_client_task_matrix;
DROP MATERIALIZED VIEW IF EXISTS session_task_stats;
DROP MATERIALIZED VIEW IF EXISTS session_client_stats;

COMMIT;
