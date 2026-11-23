-- ===========================================================================
-- File:          sql/migrations/startup/985_team_budgets.down.sql
-- Migration:     985 (rollback)
-- Database:      llm_gateway
-- Purpose:       回滚 team_budgets 表（团队月度预算）
--
-- ★ 2026-11-23 团队功能底座轮（★ 原编号 055-058 重编号为 982-985）：
--   ★ 只退**本迁移自己引入**的部分（★ 迁移链每一步只回退自己引入的那部分 ——
--     930/995/996/997 处置同款）；★ 台账回退与 up 的自登记对偶。
--   ★ 幂等：IF EXISTS ＋ DELETE ... WHERE version = '985' ⇒ 重放安全。
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

-- 删除表（CASCADE 一并带走本表上的策略 team_budgets_owner_all / team_budgets_admin_select）
DROP TABLE IF EXISTS public.team_budgets CASCADE;

-- 台账回退（与 up 的自登记对偶 —— 995/996/997 同款；down 只退自己引入的行）
DELETE FROM public.schema_migrations WHERE version = '985';

COMMIT;
