-- ===========================================================================
-- File:          sql/migrations/startup/985_team_budgets.sql
-- Migration:     985
-- Purpose:       创建 team_budgets 表：团队月度预算配置
-- Database:      llm_gateway
-- Down:          985_team_budgets.down.sql
--
-- ★ 2026-11-23 重编号（原 058_team_budgets.sql）：★ 原件**三条投递通道一条都
--   没登记** ⇒ ★ **永远不会被应用**（★ 生产实测：team_budgets 表不存在）。
--   ★ 重编号为 985（> 缺口棘轮纪元 914 ⇒ 缺口按「待部署」豁免；★ 三位数硬约束
--   见 982 头部注记）。
--
-- 前置：依赖 982_teams_table.sql（外键指向 public.teams.id）
-- 幂等：CREATE TABLE / CREATE INDEX IF NOT EXISTS
-- ===========================================================================

-- ★ 锁与语句预算守卫（994/995/996 同口径：★ 会话级 SET ⇒ docker 通道也生效）
SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

CREATE TABLE IF NOT EXISTS public.team_budgets (
    id                       bigserial PRIMARY KEY,
    team_id                  bigint NOT NULL REFERENCES public.teams(id) ON DELETE CASCADE,
    monthly_limit_credits    numeric(20, 6) NOT NULL,
    enforcement_mode         text NOT NULL DEFAULT 'warn' CHECK (enforcement_mode IN ('warn', 'hard')),
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT team_budgets_team_id_key UNIQUE (team_id)
);

CREATE INDEX IF NOT EXISTS idx_team_budgets_team_id ON public.team_budgets(team_id);

ALTER TABLE public.team_budgets ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS team_budgets_owner_all ON public.team_budgets;
CREATE POLICY team_budgets_owner_all ON public.team_budgets
    FOR ALL
    USING (
        team_id IN (
            SELECT id FROM public.teams
            WHERE owner_id = current_setting('app.current_user_id', true)::bigint
        )
    );

DROP POLICY IF EXISTS team_budgets_admin_select ON public.team_budgets;
CREATE POLICY team_budgets_admin_select ON public.team_budgets
    FOR SELECT
    USING (
        team_id IN (
            SELECT team_id FROM public.user_teams
            WHERE user_id = current_setting('app.current_user_id', true)::bigint
              AND role = 'admin'
        )
    );

COMMENT ON TABLE public.team_budgets IS '团队月度预算表：配置团队级别的预算额度和执行模式（warn/hard）';

-- 台账自登记（≥952 纪律）
INSERT INTO public.schema_migrations (version, applied_at)
VALUES ('985', now())
ON CONFLICT (version) DO NOTHING;

COMMIT;
