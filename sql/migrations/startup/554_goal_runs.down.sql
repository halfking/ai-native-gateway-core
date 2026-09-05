-- Migration 554 DOWN: 回退 goal_runs + goal_run_steps + goal_run_actions
--
-- 日期: 2026-08-21
--
-- Purpose: 清理 Wave 2-A GoalRun 持久编排账本表

BEGIN;

-- 移除 RLS 策略
DROP POLICY IF EXISTS goal_run_actions_super_admin_bypass ON goal_run_actions;
DROP POLICY IF EXISTS goal_run_actions_tenant_isolation ON goal_run_actions;
DROP POLICY IF EXISTS goal_run_steps_super_admin_bypass ON goal_run_steps;
DROP POLICY IF EXISTS goal_run_steps_tenant_isolation ON goal_run_steps;
DROP POLICY IF EXISTS goal_runs_super_admin_bypass ON goal_runs;
DROP POLICY IF EXISTS goal_runs_tenant_isolation ON goal_runs;

-- 删除表（级联删除依赖的索引与约束）
DROP TABLE IF EXISTS goal_run_actions CASCADE;
DROP TABLE IF EXISTS goal_run_steps CASCADE;
DROP TABLE IF EXISTS goal_runs CASCADE;

COMMIT;

-- POST_CONDITION:
--   1. 三表已删除：
--      SELECT COUNT(*) FROM pg_tables
--      WHERE tablename IN ('goal_runs','goal_run_steps','goal_run_actions');
--      -- 预期: 0
