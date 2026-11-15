-- Migration: Add plugin_config table for provider selection persistence
-- Date: 2026-11-15
-- Description: Stores plugin provider configuration (Native vs Casdoor selection)

-- Create plugin_config table
CREATE TABLE IF NOT EXISTS plugin_config (
    id INTEGER PRIMARY KEY DEFAULT 1,
    auth_provider VARCHAR(50) NOT NULL DEFAULT 'native',
    user_manager VARCHAR(50) NOT NULL DEFAULT 'native',
    tenant_manager VARCHAR(50) NOT NULL DEFAULT 'native',
    approval_provider VARCHAR(50) NOT NULL DEFAULT 'native',
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    
    -- Ensure only one configuration row exists (singleton pattern)
    CONSTRAINT single_config_row CHECK (id = 1),
    
    -- Validate provider names
    CONSTRAINT valid_auth_provider CHECK (auth_provider IN ('native', 'casdoor')),
    CONSTRAINT valid_user_manager CHECK (user_manager IN ('native', 'casdoor')),
    CONSTRAINT valid_tenant_manager CHECK (tenant_manager IN ('native', 'casdoor')),
    CONSTRAINT valid_approval_provider CHECK (approval_provider IN ('native', 'casdoor'))
);

-- Create index for faster lookups
CREATE INDEX IF NOT EXISTS idx_plugin_config_updated ON plugin_config(updated_at);

-- Insert default configuration (all Native providers)
INSERT INTO plugin_config (id, auth_provider, user_manager, tenant_manager, approval_provider)
VALUES (1, 'native', 'native', 'native', 'native')
ON CONFLICT (id) DO NOTHING;

-- Add comment for documentation
COMMENT ON TABLE plugin_config IS 'Plugin provider configuration - stores selection between Native (PostgreSQL) and Casdoor providers';
COMMENT ON COLUMN plugin_config.auth_provider IS 'Authentication provider: native or casdoor';
COMMENT ON COLUMN plugin_config.user_manager IS 'User management provider: native or casdoor';
COMMENT ON COLUMN plugin_config.tenant_manager IS 'Tenant management provider: native or casdoor';
COMMENT ON COLUMN plugin_config.approval_provider IS 'Approval workflow provider: native or casdoor';
