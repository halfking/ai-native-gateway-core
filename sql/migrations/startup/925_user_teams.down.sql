-- ===========================================================================
-- File:          sql/migrations/startup/925_user_teams.down.sql
-- Migration:     983 (rollback)
-- Database:      llm_gateway
-- Purpose:       回滚 user_teams 关联表（用户 ↔ 团队多对多）
--
-- ★ 2026-11-23 团队功能底座轮（★ 原编号 055-058 重编号为 918/925/973/986）：
--   ★ 只退**本迁移自己引入**的部分（★ 迁移链每一步只回退自己引入的那部分 ——
--     930/995/996/997 处置同款）；★ 台账回退与 up 的自登记对偶。
--   ★ 幂等：IF EXISTS ＋ DELETE ... WHERE version = '925' ⇒ 重放安全。
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

-- ★ 983 还在 teams 上建了一条策略（teams_member_select：成员可见自己所属团队）
--   ⇒ 退本迁移时必须一并撤掉（★ 用 to_regclass 守卫：★ teams 不在场时不报错）
DO $$
BEGIN
    IF to_regclass('public.teams') IS NOT NULL THEN
        EXECUTE 'DROP POLICY IF EXISTS teams_member_select ON public.teams';
    END IF;
END $$;

-- 删除表（CASCADE 一并带走本表上的策略 user_teams_self_select / user_teams_owner_all）
DROP TABLE IF EXISTS public.user_teams CASCADE;

-- 台账回退（与 up 的自登记对偶 —— 995/996/997 同款；down 只退自己引入的行）
DELETE FROM public.schema_migrations WHERE version = '925';

COMMIT;
