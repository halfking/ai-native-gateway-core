-- 058_team_budgets.sql
-- 创建 team_budgets 表：团队月度预算配置
-- 前置：依赖 055_teams_table.sql
-- 幂等：CREATE TABLE IF NOT EXISTS

CREATE TABLE IF NOT EXISTS team_budgets (
    id                       bigserial PRIMARY KEY,
    team_id                  bigint NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    monthly_limit_credits    numeric(20, 6) NOT NULL,
    enforcement_mode         text NOT NULL DEFAULT 'warn' CHECK (enforcement_mode IN ('warn', 'hard')),
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT team_budgets_team_id_key UNIQUE (team_id)
);

CREATE INDEX IF NOT EXISTS idx_team_budgets_team_id ON team_budgets(team_id);

ALTER TABLE team_budgets ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS team_budgets_owner_all ON team_budgets;
CREATE POLICY team_budgets_owner_all ON team_budgets
    FOR ALL
    USING (
        team_id IN (
            SELECT id FROM teams 
            WHERE owner_id = current_setting('app.current_user_id', true)::bigint
        )
    );

DROP POLICY IF EXISTS team_budgets_admin_select ON team_budgets;
CREATE POLICY team_budgets_admin_select ON team_budgets
    FOR SELECT
    USING (
        team_id IN (
            SELECT team_id FROM user_teams 
            WHERE user_id = current_setting('app.current_user_id', true)::bigint
              AND role = 'admin'
        )
    );

COMMENT ON TABLE team_budgets IS '团队月度预算表：配置团队级别的预算额度和执行模式（warn/hard）';

INSERT INTO schema_migrations (version, applied_at)
VALUES ('058', now())
ON CONFLICT (version) DO NOTHING;
