-- 965_auth_id_mapping_canonical_channel.sql
-- 2026-11-15（12h 修订审计轮 P1 收口）：Casdoor 集成的身份映射表收编
-- startup 正典通道。
--
-- ── 为什么存在本文件 ─────────────────────────────────────────────────────
--   phase3（cee143c84）把 auth_id_mapping DDL 放在顶层 migrations/036_
--   add_auth_id_mapping.sql——无投递通道（745/752 同款病，752 头注释有
--   先例登记）：文件不被 deploy pending-scanner 扫描、不在 installer
--   StartupFiles 清单、不在 boot ensure 自愈链，唯一执行方式是文档里
--   的手工 psql \i（CASDOOR_CONFIGURATION.md:189）⇒ 除手工跑过的库外
--   全部环境缺表 ⇒ 启用 casdoor provider 首登即 42P01（auth_id_mapping
--   不存在），login 500。
--
--   且 036 与 sql/migrations/startup/036_fp_slot_limit.sql **撞号异文件**
--   （顶层序号是七个月前的老段；本通道已推进到 964）。历史先例：顶层
--   db/migrations/036_fp_slot_limit 同样撞号，2026-09-27 收编 752 时
--   一并删除（752 头注释自记）。
--
-- ── 本文件相对顶层 036 的差异 ────────────────────────────────────────────
--   1. 纯幂等化（startup 通道每次部署无脑重放是安全默认——deploy-seamless
--      引擎注释原话）：
--      · CREATE INDEX → IF NOT EXISTS（顶层版裸 CREATE INDEX，重放即炸
--        42P11 duplicate index）；
--      · CREATE TRIGGER → 先 DROP IF EXISTS 再建（顶层版裸建，重放即炸
--        42710 duplicate trigger）；
--      · backfill INSERT 保留 ON CONFLICT DO NOTHING（顶层版已幂等）。
--   2. 删除 DOWN 段与验证注释段（startup 通道文件是前向幂等 DDL，
--      DOWN 走独立的 .down.sql；验证段属文档不属可执行文件）。
--   3. 表名/列/约束与顶层 036 完全一致——已在 252 等环境手工建过表的
--      环境重放本文件时全部语句 no-op，不破坏存量。
--   4. 顶层 migrations/036_add_auth_id_mapping.sql 删除（撞号异文件混淆
--      一并消除，752 先例）；.handoff/CASDOOR_CONFIGURATION.md 的手工
--      psql 步骤改指本通道（startup 通道自动投递，无需手工）。

-- ============================================================================
-- UP（幂等，重放安全）
-- ============================================================================

-- External identity mapping table
-- Maps external provider identities (Casdoor UUIDs, LDAP DNs, etc.) to internal user IDs
CREATE TABLE IF NOT EXISTS auth_id_mapping (
    internal_id SERIAL PRIMARY KEY,
    external_id TEXT NOT NULL,           -- External provider's user ID (e.g., Casdoor UUID)
    provider TEXT NOT NULL,              -- Provider name: 'native', 'casdoor', 'ldap', 'oidc'
    tenant_id TEXT NOT NULL,             -- Tenant scope for multi-tenancy
    username TEXT NOT NULL,              -- Username in the external provider
    display_name TEXT,                   -- Cached display name from provider
    email TEXT,                          -- Cached email from provider
    metadata JSONB,                      -- Provider-specific metadata (extensible)
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    last_sync_at TIMESTAMPTZ,           -- Last synchronization with external provider

    -- Unique constraints: one mapping per provider+external_id+tenant combination
    CONSTRAINT auth_id_mapping_provider_external_tenant_key UNIQUE(provider, external_id, tenant_id),
    -- Unique username per provider+tenant (prevents duplicate usernames)
    CONSTRAINT auth_id_mapping_provider_username_tenant_key UNIQUE(provider, username, tenant_id),

    -- Validation: provider must be one of the supported types
    CONSTRAINT auth_id_mapping_provider_check CHECK (provider IN ('native', 'casdoor', 'ldap', 'oidc', 'saml'))
);

-- Indexes for performance（幂等：IF NOT EXISTS）
CREATE INDEX IF NOT EXISTS idx_auth_id_mapping_external ON auth_id_mapping(external_id, provider);
CREATE INDEX IF NOT EXISTS idx_auth_id_mapping_internal ON auth_id_mapping(internal_id);
CREATE INDEX IF NOT EXISTS idx_auth_id_mapping_username ON auth_id_mapping(username, provider, tenant_id);
CREATE INDEX IF NOT EXISTS idx_auth_id_mapping_tenant ON auth_id_mapping(tenant_id);
CREATE INDEX IF NOT EXISTS idx_auth_id_mapping_provider ON auth_id_mapping(provider);

-- Auto-update timestamp trigger（幂等：先 DROP 再建）
CREATE OR REPLACE FUNCTION update_auth_id_mapping_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS auth_id_mapping_updated_at ON auth_id_mapping;
CREATE TRIGGER auth_id_mapping_updated_at
    BEFORE UPDATE ON auth_id_mapping
    FOR EACH ROW
    EXECUTE FUNCTION update_auth_id_mapping_timestamp();

-- Comments for documentation（幂等：COMMENT 本身可重放）
COMMENT ON TABLE auth_id_mapping IS 'Maps external provider identities to internal user IDs for multi-provider authentication';
COMMENT ON COLUMN auth_id_mapping.internal_id IS 'Internal sequential user ID (stable across providers)';
COMMENT ON COLUMN auth_id_mapping.external_id IS 'External provider user ID (Casdoor UUID, LDAP DN, etc.)';
COMMENT ON COLUMN auth_id_mapping.provider IS 'Authentication provider name';
COMMENT ON COLUMN auth_id_mapping.tenant_id IS 'Tenant/organization identifier';
COMMENT ON COLUMN auth_id_mapping.username IS 'Username in the external provider';
COMMENT ON COLUMN auth_id_mapping.metadata IS 'Provider-specific metadata (JSON)';
COMMENT ON COLUMN auth_id_mapping.last_sync_at IS 'Last synchronization timestamp with external provider';

-- Backfill existing native users (optional, for reporting/analytics)
-- This creates mappings for all existing PostgreSQL users with provider='native'
INSERT INTO auth_id_mapping (
    internal_id,
    external_id,
    provider,
    tenant_id,
    username,
    display_name,
    email,
    created_at,
    last_sync_at
)
SELECT
    id,
    'native:' || id::text,  -- External ID format: native:123
    'native',
    tenant_id,
    username,
    display_name,
    email,
    created_at,
    last_login_at  -- Use last_login_at as initial sync timestamp
FROM users
WHERE EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'users')
ON CONFLICT (provider, external_id, tenant_id) DO NOTHING;
