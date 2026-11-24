-- 057_api_keys_team_id.sql
-- 为 api_keys 表添加 team_id 列
-- 前置：依赖 055_teams_table.sql
-- 幂等：ADD COLUMN IF NOT EXISTS

ALTER TABLE api_keys 
    ADD COLUMN IF NOT EXISTS team_id bigint REFERENCES teams(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_api_keys_team_id ON api_keys(team_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_tenant_team ON api_keys(tenant_id, team_id) 
    WHERE team_id IS NOT NULL;

DROP POLICY IF EXISTS api_keys_team_owner_all ON api_keys;
CREATE POLICY api_keys_team_owner_all ON api_keys
    FOR ALL
    USING (
        team_id IS NOT NULL AND
        team_id IN (
            SELECT id FROM teams 
            WHERE owner_id = current_setting('app.current_user_id', true)::bigint
        )
    );

DROP POLICY IF EXISTS api_keys_team_admin_select ON api_keys;
CREATE POLICY api_keys_team_admin_select ON api_keys
    FOR SELECT
    USING (
        team_id IS NOT NULL AND
        team_id IN (
            SELECT team_id FROM user_teams 
            WHERE user_id = current_setting('app.current_user_id', true)::bigint
              AND role = 'admin'
        )
    );

COMMENT ON COLUMN api_keys.team_id IS '团队 ID（可选）：NULL 表示个人 key，非 NULL 表示团队 key';

INSERT INTO schema_migrations (version, applied_at)
VALUES ('057', now())
ON CONFLICT (version) DO NOTHING;
