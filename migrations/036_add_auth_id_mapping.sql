-- Migration: Add auth_id_mapping table for external identity mapping
-- Purpose: Enable multi-provider authentication by mapping external IDs (Casdoor, LDAP, etc.) to internal user IDs
-- Date: 2026-11-15
-- Phase: 3 (Casdoor Integration)

-- ============================================================================
-- UP Migration
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

-- Indexes for performance
CREATE INDEX idx_auth_id_mapping_external ON auth_id_mapping(external_id, provider);
CREATE INDEX idx_auth_id_mapping_internal ON auth_id_mapping(internal_id);
CREATE INDEX idx_auth_id_mapping_username ON auth_id_mapping(username, provider, tenant_id);
CREATE INDEX idx_auth_id_mapping_tenant ON auth_id_mapping(tenant_id);
CREATE INDEX idx_auth_id_mapping_provider ON auth_id_mapping(provider);

-- Auto-update timestamp trigger
CREATE OR REPLACE FUNCTION update_auth_id_mapping_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER auth_id_mapping_updated_at
    BEFORE UPDATE ON auth_id_mapping
    FOR EACH ROW
    EXECUTE FUNCTION update_auth_id_mapping_timestamp();

-- Comments for documentation
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

-- ============================================================================
-- DOWN Migration
-- ============================================================================

-- Drop trigger first
DROP TRIGGER IF EXISTS auth_id_mapping_updated_at ON auth_id_mapping;

-- Drop function
DROP FUNCTION IF EXISTS update_auth_id_mapping_timestamp();

-- Drop indexes (will be dropped automatically with table, but explicit for clarity)
DROP INDEX IF EXISTS idx_auth_id_mapping_provider;
DROP INDEX IF EXISTS idx_auth_id_mapping_tenant;
DROP INDEX IF EXISTS idx_auth_id_mapping_username;
DROP INDEX IF EXISTS idx_auth_id_mapping_internal;
DROP INDEX IF EXISTS idx_auth_id_mapping_external;

-- Drop table
DROP TABLE IF EXISTS auth_id_mapping;

-- ============================================================================
-- Verification Queries (for testing)
-- ============================================================================

-- Run these after migration to verify:

-- 1. Check table structure
-- SELECT column_name, data_type, is_nullable, column_default
-- FROM information_schema.columns
-- WHERE table_name = 'auth_id_mapping'
-- ORDER BY ordinal_position;

-- 2. Check indexes
-- SELECT indexname, indexdef
-- FROM pg_indexes
-- WHERE tablename = 'auth_id_mapping';

-- 3. Check backfilled native users
-- SELECT COUNT(*) as native_user_count
-- FROM auth_id_mapping
-- WHERE provider = 'native';

-- 4. Verify uniqueness constraints work
-- INSERT INTO auth_id_mapping (external_id, provider, tenant_id, username)
-- VALUES ('test-1', 'native', 'default', 'testuser');
-- -- Second insert with same key should fail:
-- INSERT INTO auth_id_mapping (external_id, provider, tenant_id, username)
-- VALUES ('test-1', 'native', 'default', 'testuser');  -- Should error
