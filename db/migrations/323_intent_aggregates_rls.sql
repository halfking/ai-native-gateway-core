-- 323_intent_aggregates_rls.sql — Add RLS to public.intent_aggregates
--
-- Purpose: lint-pg-rls L1=2 → 0 (2026-07-01 audit).
-- Companion: 309_intent_aggregates.sql created the table without RLS.
-- The `intent_aggregates` table is the V4 治理平台 IntentWorker output:
-- per-(tenant_id, intent_kind) count rollup. PRIMARY KEY (tenant_id, intent_kind).
-- Without RLS, an admin query that forgets `WHERE tenant_id = $1` would leak
-- other tenants' intent classification counts.
--
-- Design:
--   1. tenant_isolation: composite-PK filter via the GUC tenant.
--   2. super_admin_bypass: same convention as 322 (app.bypass_rls / app.current_role).
--
-- Writer audit (must SET LOCAL app.bypass_rls = 'true' before INSERT):
--   - domains/assets/intent_store.go: PGIntentAggregateStore.Increment
--     Currently uses s.pool.Exec() without setting GUC — must wrap in tx with
--     `SET LOCAL app.bypass_rls = 'true'`, OR refactor to withTenantTx.
--
-- Idempotent: DROP POLICY IF EXISTS + ALTER TABLE ENABLE RLS.

BEGIN;

ALTER TABLE public.intent_aggregates ENABLE ROW LEVEL SECURITY;

-- Policy 1: tenant isolation.
DROP POLICY IF EXISTS tenant_isolation_intent_aggregates ON public.intent_aggregates;
CREATE POLICY tenant_isolation_intent_aggregates ON public.intent_aggregates
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- Policy 2: super_admin bypass.
DROP POLICY IF EXISTS intent_aggregates_super_admin_bypass ON public.intent_aggregates;
CREATE POLICY intent_aggregates_super_admin_bypass ON public.intent_aggregates
    USING (
        current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    );

COMMIT;
