-- Migration 001: Users table for multi-tenant admin auth
-- Idempotent: uses IF NOT EXISTS, safe to run multiple times.

CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL DEFAULT 'default',
    username VARCHAR(128) NOT NULL UNIQUE,
    password_hash VARCHAR(256) NOT NULL,
    display_name VARCHAR(128) NOT NULL DEFAULT '',
    email VARCHAR(256) NOT NULL DEFAULT '',
    role VARCHAR(32) NOT NULL DEFAULT 'tenant_admin',  -- super_admin | tenant_admin
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_users_tenant ON users(tenant_id);
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
