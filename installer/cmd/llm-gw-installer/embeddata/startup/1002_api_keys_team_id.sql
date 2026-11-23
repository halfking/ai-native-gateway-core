-- ===========================================================================
-- File:          sql/migrations/startup/973_api_keys_team_id.sql
-- Migration:     984
-- Purpose:       为 api_keys 表添加 team_id 列（团队 key 归属）
-- Database:      llm_gateway
-- Down:          973_api_keys_team_id.down.sql
--
-- ★ 2026-11-23 重编号（原 057_api_keys_team_id.sql）：★ 原件**三条投递通道一条
--   都没登记** ⇒ ★ **永远不会被应用**（★ 生产实测：api_keys 无 team_id 列）
--   ⇒ ★ 而现役二进制含 `INSERT INTO api_keys (…, key_alias, team_id)`
--   ⇒ ⇒ ★ **生产上「创建 API key」必然失败**（★ 本迁移就是那个 P0 的本体）。
--   ★ 重编号为 984（> 缺口棘轮纪元 914 ⇒ 缺口按「待部署」豁免；★ 三位数硬约束
--   见 982 头部注记）。
--
-- 前置：依赖 918_teams_table.sql（外键指向 public.teams.id）；
--       api_keys 表本身由基线 01-schema.sql 提供
-- 幂等：ADD COLUMN / CREATE INDEX IF NOT EXISTS
--
-- ★ RLS 口径：★ 本迁移**只备策略、不开 RLS** —— api_keys 是**基线表**，
--   而「api_keys 是否 ENABLE ROW LEVEL SECURITY」是**已登记的 owner 裁决项**
--   ⇒ ⇒ ★ 迁移不替 owner 决定；★ 但策略先备好（★ 一旦 owner 拍板开 RLS，
--   策略即时生效）。
-- ===========================================================================

-- ★ 锁与语句预算守卫（994/995/996 同口径：★ 会话级 SET ⇒ docker 通道也生效）
SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

ALTER TABLE public.api_keys
    ADD COLUMN IF NOT EXISTS team_id bigint REFERENCES public.teams(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_api_keys_team_id ON public.api_keys(team_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_tenant_team ON public.api_keys(tenant_id, team_id)
    WHERE team_id IS NOT NULL;

DROP POLICY IF EXISTS api_keys_team_owner_all ON public.api_keys;
CREATE POLICY api_keys_team_owner_all ON public.api_keys
    FOR ALL
    USING (
        team_id IS NOT NULL AND
        team_id IN (
            SELECT id FROM public.teams
            WHERE owner_id = current_setting('app.current_user_id', true)::bigint
        )
    );

DROP POLICY IF EXISTS api_keys_team_admin_select ON public.api_keys;
CREATE POLICY api_keys_team_admin_select ON public.api_keys
    FOR SELECT
    USING (
        team_id IS NOT NULL AND
        team_id IN (
            SELECT team_id FROM public.user_teams
            WHERE user_id = current_setting('app.current_user_id', true)::bigint
              AND role = 'admin'
        )
    );

COMMENT ON COLUMN public.api_keys.team_id IS '团队 ID（可选）：NULL 表示个人 key，非 NULL 表示团队 key';

-- 台账自登记（≥952 纪律）
INSERT INTO public.schema_migrations (version, applied_at)
VALUES ('973', now())
ON CONFLICT (version) DO NOTHING;

COMMIT;
