-- ===========================================================================
-- File:          sql/migrations/startup/918_teams_table.down.sql
-- Migration:     982 (rollback)
-- Database:      llm_gateway
-- Purpose:       回滚 teams 表（团队组织单元）
--
-- ★ 2026-11-23 团队功能底座轮（★ 原编号 055-058 重编号为 918/925/973/986）：
--   ★ 只退**本迁移自己引入**的部分（★ 迁移链每一步只回退自己引入的那部分 ——
--     930/995/996/997 处置同款）；★ 台账回退与 up 的自登记对偶。
--   ★ 幂等：IF EXISTS ＋ DELETE ... WHERE version = '918' ⇒ 重放安全。
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

-- 删除表（★ CASCADE 一并带走本表上的策略 teams_owner_all）
DROP TABLE IF EXISTS public.teams CASCADE;

-- 台账回退（与 up 的自登记对偶 —— 995/996/997 同款；down 只退自己引入的行）
DELETE FROM public.schema_migrations WHERE version = '918';

COMMIT;
