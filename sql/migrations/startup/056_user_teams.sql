-- 056_user_teams.sql
-- 创建 user_teams 关联表：用户与团队的多对多关系，支持角色（owner/admin/member）
-- 前置：依赖 055_teams_table.sql
-- 幂等：CREATE TABLE IF NOT EXISTS

CREATE TABLE IF NOT EXISTS user_teams (
    id         bigserial PRIMARY KEY,
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id    bigint NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    role       text NOT NULL DEFAULT 'member',
    joined_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_teams_user_team_key UNIQUE (user_id, team_id),
    CONSTRAINT user_teams_role_check CHECK (role IN ('owner', 'admin', 'member'))
);

CREATE INDEX IF NOT EXISTS idx_user_teams_user_id ON user_teams(user_id);
CREATE INDEX IF NOT EXISTS idx_user_teams_team_id ON user_teams(team_id);

ALTER TABLE user_teams ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS user_teams_self_select ON user_teams;
CREATE POLICY user_teams_self_select ON user_teams
    FOR SELECT
    USING (user_id = current_setting('app.current_user_id', true)::bigint);

DROP POLICY IF EXISTS user_teams_owner_all ON user_teams;
CREATE POLICY user_teams_owner_all ON user_teams
    FOR ALL
    USING (
        team_id IN (
            SELECT id FROM teams 
            WHERE owner_id = current_setting('app.current_user_id', true)::bigint
        )
    );

DROP POLICY IF EXISTS teams_member_select ON teams;
CREATE POLICY teams_member_select ON teams
    FOR SELECT
    USING (
        id IN (
            SELECT team_id FROM user_teams 
            WHERE user_id = current_setting('app.current_user_id', true)::bigint
        )
    );

COMMENT ON TABLE user_teams IS '用户-团队关联表：多对多关系，支持角色（owner/admin/member）';

INSERT INTO schema_migrations (version, applied_at)
VALUES ('056', now())
ON CONFLICT (version) DO NOTHING;
