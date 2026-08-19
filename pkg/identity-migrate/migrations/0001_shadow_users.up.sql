-- 0001_shadow_users.up.sql
-- 跨项目统一身份主表（owner = kxuser，不开 RLS，与 memora audit.* 同模式）

CREATE TABLE IF NOT EXISTS shadow_users (
    shadow_user_id     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    canonical_user_id  UUID NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'active',
    display_name       TEXT NOT NULL DEFAULT '',
    primary_email      TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_shadow_users_canonical ON shadow_users(canonical_user_id);
CREATE INDEX IF NOT EXISTS idx_shadow_users_email ON shadow_users(primary_email) WHERE primary_email <> '';
CREATE INDEX IF NOT EXISTS idx_shadow_users_status ON shadow_users(status) WHERE status <> 'active';

-- 记录迁移所有权（与 memora kxmemory_migration_ownership 同模式）
CREATE TABLE IF NOT EXISTS identity_migration_ownership (
    version          INTEGER PRIMARY KEY,
    name             TEXT NOT NULL,
    applied_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    checksum         TEXT NOT NULL DEFAULT '',
    applied_by       TEXT NOT NULL DEFAULT current_user
);