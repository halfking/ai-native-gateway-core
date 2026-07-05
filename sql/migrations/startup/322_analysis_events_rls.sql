-- 322_analysis_events_rls.sql — Add RLS to public.analysis_events
--
-- Purpose: lint-pg-rls L1=2 → 0 (2026-07-01 audit).
-- Companion: 306_analysis_events.sql created the table without RLS.
-- The `analysis_events` table is the V4 治理平台 async analysis event bus;
-- every row carries `tenant_id NOT NULL` (see 306.sql:23). Without RLS,
-- a single missed `WHERE tenant_id = $1` in a query would leak cross-tenant
-- event payloads (which may include LLM prompts/responses).
--
-- Design:
--   1. tenant_isolation: filter rows to the caller's tenant (default GUC).
--   2. super_admin_bypass: allow writers / admin tools to set
--      `SET LOCAL app.bypass_rls = 'true'` to write/read across tenants.
--      This matches the convention from 316_output_compliance_monitoring.sql
--      (output_compliance_audit_super_admin) and 315_prompt_injection_detection.sql
--      (prompt_injection_audit_super_admin).
--
-- Writer audit (must SET LOCAL app.bypass_rls = 'true' before INSERT):
--   - domains/analysis/bus/publisher.go: PGPublisher.Publish
--     Currently uses p.pool.Exec() without setting GUC — must wrap in tx with
--     `SET LOCAL app.bypass_rls = 'true'`, OR refactor to withTenantTx.
--
-- Idempotent: DROP POLICY IF EXISTS + ALTER TABLE IF NOT EXISTS-equivalent
-- (ALTER TABLE ... ENABLE RLS is idempotent on a PG table).

BEGIN;

-- Defense-in-depth at the DB layer.
ALTER TABLE public.analysis_events ENABLE ROW LEVEL SECURITY;

-- Policy 1: tenant-isolated reads/writes (default path).
DROP POLICY IF EXISTS tenant_isolation_analysis_events ON public.analysis_events;
CREATE POLICY tenant_isolation_analysis_events ON public.analysis_events
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- Policy 2: super_admin bypass — writers / admin tooling set
-- `SET LOCAL app.bypass_rls = 'true'` to opt out of the tenant filter.
-- Matches 316_output_compliance_monitoring.sql convention.
DROP POLICY IF EXISTS analysis_events_super_admin_bypass ON public.analysis_events;
CREATE POLICY analysis_events_super_admin_bypass ON public.analysis_events
    USING (
        current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    );

COMMIT;
