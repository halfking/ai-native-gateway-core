-- ===========================================================================
-- File:          sql/migrations/startup/997_tasks_dimension_table.down.sql
-- Migration:     997 (rollback)
-- Database:      llm_gateway
-- Purpose:       回滚任务维表创建
--
-- Date: 2026-11-21
-- Author: Kiro AI
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';
-- 删除触发器
DROP TRIGGER IF EXISTS trigger_tasks_updated_at ON public.tasks;
DROP FUNCTION IF EXISTS public.update_tasks_updated_at();

-- 删除表 (CASCADE 会同时删除依赖的外键约束)
DROP TABLE IF EXISTS public.tasks CASCADE;

COMMIT;
