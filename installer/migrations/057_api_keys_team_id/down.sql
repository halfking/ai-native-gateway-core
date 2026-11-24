-- ===========================================================================
-- File:          sql/migrations/startup/057_api_keys_team_id.down.sql
-- Migration:     984 (rollback)
-- Database:      llm_gateway
-- Purpose:       回滚 api_keys.team_id 列（团队 key 归属）
--
-- ★ 2026-11-23 团队功能底座轮（★ 原编号 055-058 重编号为 918/925/973/986）：
--   ★ 只退**本迁移自己引入**的部分（★ 迁移链每一步只回退自己引入的那部分 ——
--     930/995/996/997 处置同款）；★ 台账回退与 up 的自登记对偶。
--   ★ 幂等：IF EXISTS ＋ DELETE ... WHERE version = '057' ⇒ 重放安全。
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

-- ★ 984 在 api_keys 上建了两条策略（api_keys_team_owner_all / api_keys_team_admin_select）
--   ⇒ 退本迁移时必须先撤策略、★ 再退列（★ to_regclass 守卫：★ api_keys 不在场时不报错）
DO $$
BEGIN
    IF to_regclass('public.api_keys') IS NOT NULL THEN
        EXECUTE 'DROP POLICY IF EXISTS api_keys_team_owner_all ON public.api_keys';
        EXECUTE 'DROP POLICY IF EXISTS api_keys_team_admin_select ON public.api_keys';
    END IF;
END $$;

-- 删除列（★ 注意：★ 一旦该列已有数据、★ 退列会丢数据 ⇒ 优先向前修）
ALTER TABLE public.api_keys DROP COLUMN IF EXISTS team_id;

-- 台账回退（与 up 的自登记对偶 —— 995/996/997 同款；down 只退自己引入的行）
DELETE FROM public.schema_migrations WHERE version = '057';

COMMIT;
