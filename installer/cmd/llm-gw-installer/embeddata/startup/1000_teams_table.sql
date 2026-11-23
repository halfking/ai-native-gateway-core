-- ===========================================================================
-- File:          sql/migrations/startup/918_teams_table.sql
-- Migration:     982
-- Purpose:       创建 teams 表：团队组织单元，支持多租户场景下的团队层级预算管理
-- Database:      llm_gateway
-- Down:          918_teams_table.down.sql
--
-- ★ 2026-11-23 重编号（原 055_teams_table.sql）：★ 原编号 055 落在「对账地板
--   412」以下、★ 且三条投递通道（installer 的 StartupFiles / 211 seamless 的
--   files=(...) 数组 / 存量升级通道的低版本清单）**一条都没登记** ⇒ ★ ★
--   **原件永远不会被应用**（★ 2026-11-23 生产实测：`teams` 表不存在）⇒ ★
--   而现役代码已无条件引用它。
--   ★ 重编号为 918：① > 缺口棘轮纪元 914 ⇒ 缺口按「待部署」豁免、★ 不计真缺口；
--     ② **三位数是硬约束**（migrationNumPrefix=^[0-9]{3}_ ⇒ 四位数对全部守卫
--     隐形 —— 996 行同款注记）；③ canonical 目录里 982-989 全空 ⇒ 取 918/925/973/986（★ 2026-11-23 二次避让：曾用 982-985 与上游 provider_call_attempts 撞号 ⇒ 按 919 让号轮先例改占真空号）。
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
VALUES ('918', now())
ON CONFLICT (version) DO NOTHING;

COMMIT;
