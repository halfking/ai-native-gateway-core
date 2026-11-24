-- 055_teams_table.sql
-- 创建 teams 表：团队组织单元，支持多租户场景下的团队层级预算管理
-- 前置：依赖 users 表
-- 幂等：CREATE TABLE IF NOT EXISTS

CREATE TABLE IF NOT EXISTS teams (
    id            bigserial PRIMARY KEY,
    name          text NOT NULL,
    display_name  text,
    owner_id      bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    tenant_id     text NOT NULL,
    description   text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT teams_tenant_name_key UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS idx_teams_owner_id ON teams(owner_id);
CREATE INDEX IF NOT EXISTS idx_teams_tenant_id ON teams(tenant_id);

ALTER TABLE teams ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS teams_owner_all ON teams;
CREATE POLICY teams_owner_all ON teams
    FOR ALL
    USING (owner_id = current_setting('app.current_user_id', true)::bigint);

COMMENT ON TABLE teams IS '团队表：支持租户内团队层级的组织结构，用于团队预算和成员管理';
COMMENT ON COLUMN teams.id IS '主键';
COMMENT ON COLUMN teams.name IS '团队标识名（URL-safe，API 路径使用）';
COMMENT ON COLUMN teams.display_name IS '团队显示名（前端展示）';
COMMENT ON COLUMN teams.owner_id IS '团队所有者（users.id），拥有完全管理权限';
COMMENT ON COLUMN teams.tenant_id IS '租户 ID（多租户隔离）';

INSERT INTO schema_migrations (version, applied_at)
VALUES ('055', now())
ON CONFLICT (version) DO NOTHING;
