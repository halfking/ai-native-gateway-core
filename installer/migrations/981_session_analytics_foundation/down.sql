-- Migration 981 Rollback: Session Analytics Foundation
-- 回滚会话分析系统的基础设施（幂等：每条语句都带 IF EXISTS，重复执行无副作用）
--
-- ★ 回滚顺序：先删函数（视图的刷新依赖），再删视图，再删索引，最后删列。
--   sessions 上的四列是**数据列**（is_probe 是回填出来的标记），回滚会丢掉
--   is_probe 的标记状态 —— 若运维手工标记过重要会话，回滚前请自行留档。
--
-- ★ lock_timeout 用会话级 SET（不是 SET LOCAL）：本文件可能跑在非事务流式通道
--   （psql -f -，无 --single-transaction）⇒ SET LOCAL 在那里只报 WARNING 且不生效
--   ⇒ 守卫会静默失效（实测见 up.sql 同段注释）。
SET lock_timeout = '5s';

-- ============================================================================
-- Part 1: 删除刷新函数
-- ============================================================================
-- ★ 顺序至关重要（实测踩到）：判定函数 session_analytics_is_probe 被**四个物化视图**
--   依赖（视图定义里调用它）⇒ 先删函数会报
--   "cannot drop function session_analytics_is_probe because other objects depend on it
--    / materialized view ... depends on function"。
--   ⇒ 必须是：先删视图 → 再删判定函数。本文件严格按此序。

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
-- Part 3: 删除探测判定函数（视图已删 ⇒ 无依赖）
-- ============================================================================

DROP FUNCTION IF EXISTS session_analytics_is_probe(TEXT, TEXT, TEXT);

-- ============================================================================
-- Part 4: 删除 sessions 表索引
-- ============================================================================

DROP INDEX IF EXISTS idx_sessions_is_probe;
DROP INDEX IF EXISTS idx_sessions_task_category;
DROP INDEX IF EXISTS idx_sessions_project;

-- ============================================================================
-- Part 5: 删除 sessions 表元数据列
-- ============================================================================

ALTER TABLE sessions DROP COLUMN IF EXISTS is_probe;
ALTER TABLE sessions DROP COLUMN IF EXISTS task_category;
ALTER TABLE sessions DROP COLUMN IF EXISTS task;
ALTER TABLE sessions DROP COLUMN IF EXISTS project;

-- ============================================================================
-- 完成
-- ============================================================================
