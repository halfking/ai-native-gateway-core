-- 047_apihub_assets.sql — API Hub unified asset registry (NOW-1 / A0)
--
-- Consolidates LLM endpoints, MCP servers, and (future) Agents into a single
-- "asset" table. The composite primary key (kind, ref_id) lets different
-- source tables share the namespace without collision: model_offers.id=5 and
-- tool_registry.id=5 coexist as distinct assets.
--
-- Companion: 048_apihub_relationships.sql (topology edges).
-- Go package: services/llm-gateway-go/apihub/  (Service + Store interface).
--
-- RLS: enabled, using public.get_current_tenant() (same convention as
-- migration 026). The Go Service derives tenant_id from the authenticated
-- context (apihub.WithTenant), never from a request body — an attacker
-- cannot forge tenant_id.
--
-- Idempotent: safe to re-run (DROP ... IF EXISTS guards).

BEGIN;

CREATE TABLE IF NOT EXISTS public.assets (
    kind           text        NOT NULL,
    ref_id         bigint      NOT NULL,
    tenant_id      text        NOT NULL,
    name           text        NOT NULL,
    owner          text,
    team           text,
    cost_center    text,
    tags           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    health_state   text        NOT NULL DEFAULT 'unknown',
    version        text        NOT NULL DEFAULT '0.0.0',
    registered_at  timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz,
    metadata       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT pk_assets PRIMARY KEY (kind, ref_id),
    CONSTRAINT chk_assets_kind CHECK (kind IN ('llm_endpoint', 'mcp_server', 'agent')),
    CONSTRAINT chk_assets_health CHECK (health_state IN ('healthy', 'degraded', 'down', 'unknown'))
);

-- Tenant-scoped lookups (the hot path: List by tenant + kind).
CREATE INDEX IF NOT EXISTS idx_assets_tenant_kind
    ON public.assets (tenant_id, kind);

-- Tag-based filtering (GIN for jsonb containment queries).
CREATE INDEX IF NOT EXISTS idx_assets_tags
    ON public.assets USING gin (tags jsonb_path_ops);

-- RLS: a tenant can only see its own assets.
ALTER TABLE public.assets ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_assets ON public.assets;
CREATE POLICY tenant_isolation_assets ON public.assets
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

COMMIT;

-- +migrate Down
-- Rollback script for 047_apihub_assets.sql
-- Removes the assets table and its indexes/policies.
-- Safe to run even if the table does not exist (IF EXISTS guards).

BEGIN;

DROP POLICY IF EXISTS tenant_isolation_assets ON public.assets;
DROP INDEX IF EXISTS idx_assets_tags;
DROP INDEX IF EXISTS idx_assets_tenant_kind;
DROP TABLE IF EXISTS public.assets;

COMMIT;
