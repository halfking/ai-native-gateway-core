-- Migration 006: Tenants table for multi-tenant lifecycle management
-- Idempotent: uses IF NOT EXISTS, safe to run multiple times.
--
-- Adds:
--   tenants table: code (PK), name, status (5-state enum), description,
--   contact_email, created_at, updated_at
--   idx_tenants_status index for status filtering

CREATE TABLE IF NOT EXISTS tenants (
    code VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'trial', 'suspended', 'expired', 'disabled')),
    description TEXT NOT NULL DEFAULT '',
    contact_email VARCHAR(256) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tenants_status ON tenants(status);
CREATE INDEX IF NOT EXISTS idx_tenants_name ON tenants(name);
