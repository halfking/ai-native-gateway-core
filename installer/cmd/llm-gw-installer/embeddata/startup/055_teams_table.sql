-- ===========================================================================
-- File:          sql/migrations/startup/055_teams_table.sql
-- Migration:     055
-- Purpose:       创建 teams 表：团队组织单元，支持多租户场景下的团队层级预算管理
-- Database:      llm_gateway
-- Down:          055_teams_table.down.sql
--
-- ★ 2026-11-24 弃用重编号轮：★ 曾重编号为 918，★ 但该号段已被上游复用于别的迁移
--   （★ 986/925 各有两份 ⇒ 撞号）⇒ ★★ 回到原号 055 ⇒ ★★ **内容保留我方那版
--   的严格超集**（★ 事务包裹 ＋ public. schema 限定 ＋ RLS）★ ★★
--
-- 前置：依赖 users 表（由基线 01-schema.sql 提供）
-- 幂等：CREATE TABLE / CREATE INDEX IF NOT EXISTS
-- ===========================================================================

-- ★ 锁与语句预算守卫（994/995/996 同口径）：★ 会话级 SET（★ docker 流式通道上
--   SET LOCAL 静默失效 —— 994 实测先例）⇒ ★ 等锁最多 5s、单语句最多 10min。
SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

CREATE TABLE IF NOT EXISTS public.teams (
    id            bigserial PRIMARY KEY,
    name          text NOT NULL,
    display_name  text,
    owner_id      bigint NOT NULL REFERENCES public.users(id) ON DELETE RESTRICT,
    tenant_id     text NOT NULL,
    description   text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT teams_tenant_name_key UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS idx_teams_owner_id ON public.teams(owner_id);
CREATE INDEX IF NOT EXISTS idx_teams_tenant_id ON public.teams(tenant_id);

ALTER TABLE public.teams ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS teams_owner_all ON public.teams;
CREATE POLICY teams_owner_all ON public.teams
    FOR ALL
    USING (owner_id = current_setting('app.current_user_id', true)::bigint);

COMMENT ON TABLE public.teams IS '团队表：支持租户内团队层级的组织结构，用于团队预算和成员管理';
COMMENT ON COLUMN public.teams.id IS '主键';
COMMENT ON COLUMN public.teams.name IS '团队标识名（URL-safe，API 路径使用）';
COMMENT ON COLUMN public.teams.display_name IS '团队显示名（前端展示）';
COMMENT ON COLUMN public.teams.owner_id IS '团队所有者（users.id），拥有完全管理权限';
COMMENT ON COLUMN public.teams.tenant_id IS '租户 ID（多租户隔离）';

-- 台账自登记（≥952 纪律：迁移必须自登记 ⇒ 否则「按账本判断有没有做」的人会踩空）
INSERT INTO public.schema_migrations (version, applied_at)
VALUES ('055', now())
ON CONFLICT (version) DO NOTHING;

COMMIT;
