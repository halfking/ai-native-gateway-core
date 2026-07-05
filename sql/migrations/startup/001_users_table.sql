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

--
-- Round 33 (2026-06-16) — Pattern A (tenant_id) RLS for llm-gateway-go.
-- Mirrors brandmind-go's RLS pattern (uses the same app.current_tenant
-- GUC + public.get_current_tenant() helper). Defined here (the first
-- migration) so subsequent migrations can reference the helper.
--
-- Session variable convention: app.current_tenant is set per connection
-- by the application (analogous to brandmind-go). Helper function
-- returns the GUC's value, coalescing unset to 'default' so superuser
-- connections (which bypass RLS anyway) and unset cases don't error.
--
-- Note: llm-gateway-go also uses app.current_admin (in db.go:62) for
-- admin operations. For RLS purposes we use a separate GUC because
-- the tenant scope is the data layer and admin is the audit context.
--
CREATE OR REPLACE FUNCTION public.get_current_tenant()
RETURNS text
LANGUAGE sql
STABLE
AS $body$
  SELECT COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default');
$body$;

-- Users: every tenant has its own user pool. RLS filters by tenant_id.
-- Uses DROP IF EXISTS + CREATE POLICY for idempotency (PG has no CREATE OR REPLACE POLICY).
ALTER TABLE public.users ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_users ON public.users;
CREATE POLICY tenant_isolation_users ON public.users
  USING ((tenant_id)::text = (public.get_current_tenant())::text);
