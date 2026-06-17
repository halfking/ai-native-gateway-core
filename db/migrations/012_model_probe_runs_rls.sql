-- 012_model_probe_runs_rls.sql
-- Round 47 (2026-06-18) — close L1 multi-tenant leak:
--   model_probe_runs has tenant_id but no ENABLE ROW LEVEL SECURITY.
--   Discovered by `make -C scripts lint-pg-rls` while preparing
--   compression v7 T1 schema work (docs/llm-gateway-go/2026-06-18-compression-v7-final.md).
--
-- Why this matters:
--   - table contains credential_id + raw_model_name + error_message + state_change
--     (which downstream code uses for cross-tenant "model probe history" panels)
--   - without RLS, a tenant_admin query like
--       SELECT * FROM model_probe_runs WHERE credential_id=?
--     can return rows for OTHER tenants' probes on the same shared credential
--     (multi-tenant credentials are common: shared model_offers, shared free pool)
--
-- Pattern: same as request_logs (007_maas_billing.sql line 121):
--   ENABLE ROW LEVEL SECURITY
--   CREATE POLICY tenant_isolation_<table>
--     USING ((tenant_id)::text = (public.get_current_tenant())::text)
--
-- Idempotency: DROP POLICY IF EXISTS before CREATE — matches existing 007/008 style.
-- Backfill: not needed (tenant_id is NOT NULL DEFAULT 'default' from 010).

ALTER TABLE public.model_probe_runs ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation_model_probe_runs ON public.model_probe_runs;

CREATE POLICY tenant_isolation_model_probe_runs ON public.model_probe_runs
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

COMMENT ON POLICY tenant_isolation_model_probe_runs ON public.model_probe_runs IS
    'Round 47 (2026-06-18): per-tenant isolation for probe history. Closes L1 leak discovered by lint-pg-rls during v7 T1 prep. Required by docs/multi-tenant-standards.md §3.2 (Pattern A: tenant_id column requires ENABLE ROW LEVEL SECURITY).';