-- ===========================================================================
-- File:          sql/migrations/startup/925_user_teams.sql
-- Migration:     983
-- Purpose:       创建 user_teams 关联表：用户与团队的多对多关系，支持角色（owner/admin/member）
-- Database:      llm_gateway
-- Down:          925_user_teams.down.sql
--
-- ★ 2026-11-23 重编号（原 056_user_teams.sql）：★ 原件**三条投递通道一条都没
--   登记** ⇒ ★ **永远不会被应用**（★ 生产实测：user_teams 表不存在）。
--   ★ 重编号为 983（> 缺口棘轮纪元 914 ⇒ 缺口按「待部署」豁免；★ 三位数硬约束
--   见 982 头部注记）。
--
-- 前置：依赖 918_teams_table.sql（外键指向 public.teams.id）
-- 幂等：CREATE TABLE / CREATE INDEX IF NOT EXISTS
-- ===========================================================================

-- ★ 锁与语句预算守卫（994/995/996 同口径：★ 会话级 SET ⇒ docker 通道也生效）
SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

CREATE TABLE IF NOT EXISTS public.user_teams (
    id         bigserial PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    team_id    bigint NOT NULL REFERENCES public.teams(id) ON DELETE CASCADE,
    role       text NOT NULL DEFAULT 'member',
    joined_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_teams_user_team_key UNIQUE (user_id, team_id),
    CONSTRAINT user_teams_role_check CHECK (role IN ('owner', 'admin', 'member'))
);

CREATE INDEX IF NOT EXISTS idx_user_teams_user_id ON public.user_teams(user_id);
CREATE INDEX IF NOT EXISTS idx_user_teams_team_id ON public.user_teams(team_id);

ALTER TABLE public.user_teams ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS user_teams_self_select ON public.user_teams;
CREATE POLICY user_teams_self_select ON public.user_teams
    FOR SELECT
    USING (user_id = current_setting('app.current_user_id', true)::bigint);

DROP POLICY IF EXISTS user_teams_owner_all ON public.user_teams;
CREATE POLICY user_teams_owner_all ON public.user_teams
    FOR ALL
    USING (
        team_id IN (
            SELECT id FROM public.teams
            WHERE owner_id = current_setting('app.current_user_id', true)::bigint
        )
    );

-- ★ 成员可见自己所属团队（★ 策略建在 teams 上、★ 由本迁移引入 ⇒ down 时必须撤掉）
DROP POLICY IF EXISTS teams_member_select ON public.teams;
CREATE POLICY teams_member_select ON public.teams
    FOR SELECT
    USING (
        id IN (
            SELECT team_id FROM public.user_teams
            WHERE user_id = current_setting('app.current_user_id', true)::bigint
        )
    );

COMMENT ON TABLE public.user_teams IS '用户-团队关联表：用户与团队的多对多关系，支持角色（owner/admin/member）';

-- 台账自登记（≥952 纪律）
INSERT INTO public.schema_migrations (version, applied_at)
VALUES ('925', now())
ON CONFLICT (version) DO NOTHING;

COMMIT;
