-- Migration 980 Rollback: Session Analytics Foundation
-- 回滚会话分析系统的基础设施

-- ============================================================================
-- Part 1: 删除辅助函数
-- ============================================================================

DROP FUNCTION IF EXISTS refresh_all_session_analytics_views();
DROP FUNCTION IF EXISTS refresh_session_analytics_view(TEXT);

-- ============================================================================
-- Part 2: 删除物化视图
-- ============================================================================

DROP MATERIALIZED VIEW IF EXISTS model_usage_aggregates;
DROP MATERIALIZED VIEW IF EXISTS agent_usage_aggregates;
DROP MATERIALIZED VIEW IF EXISTS project_task_aggregates;
DROP MATERIALIZED VIEW IF EXISTS user_session_aggregates;

-- ============================================================================
-- Part 3: 删除 sessions 表索引
-- ============================================================================

DROP INDEX IF EXISTS idx_sessions_is_probe;
DROP INDEX IF EXISTS idx_sessions_task_category;
DROP INDEX IF EXISTS idx_sessions_project;

-- ============================================================================
-- Part 4: 删除 sessions 表元数据列
-- ============================================================================

ALTER TABLE sessions DROP COLUMN IF EXISTS is_probe;
ALTER TABLE sessions DROP COLUMN IF EXISTS task_category;
ALTER TABLE sessions DROP COLUMN IF EXISTS task;
ALTER TABLE sessions DROP COLUMN IF EXISTS project;

-- ============================================================================
-- 完成
-- ============================================================================
