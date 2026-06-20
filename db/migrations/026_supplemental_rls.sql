-- Round 48 (2026-06-21) — supplemental RLS for tenant-scoped tables
-- whose CREATE TABLE statements live in earlier migrations.
--
-- Why this is a separate migration instead of being appended to the
-- original migrations:
--
--   1. tenant_settings_kv lives in 022_settings_kv.sql (settings-
--      management project, untouched).
--   2. settings_audit lives in 023_settings_audit.sql (same).
--   3. tenant_tool_policies, tool_call_events, tool_usage_stats live
--      in 025_tool_registry_enhancements.sql (tool-registry team,
--      also untouched).
--
-- Per the deployment protocol for Round 48, we ship a single RLS-
-- supplement migration rather than editing files owned by other
-- projects.  Each CREATE TABLE statement above is accompanied by
-- the corresponding ALTER TABLE ... ENABLE ROW LEVEL SECURITY +
-- CREATE POLICY here, idempotently (DROP POLICY IF EXISTS guard).
--
-- Without this migration, pg-rls-lint flags L1 for the three
-- tables in 025 and the llm-gateway-go baseline test fails.

-- ── From migration 022 ─────────────────────────────────────────────
ALTER TABLE tenant_settings_kv ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tenant_settings_kv ON public.tenant_settings_kv;
CREATE POLICY tenant_isolation_tenant_settings_kv ON public.tenant_settings_kv
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- ── From migration 023 ─────────────────────────────────────────────
-- tenant_id is NULLABLE on settings_audit (platform-level settings
-- have no tenant). The policy allows NULL rows so platform writes
-- remain visible to all callers in the default tenant context.
ALTER TABLE settings_audit ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_settings_audit ON public.settings_audit;
CREATE POLICY tenant_isolation_settings_audit ON public.settings_audit
    USING ((tenant_id)::text = (public.get_current_tenant())::text
           OR (tenant_id) IS NULL);

-- ── From migration 025 ─────────────────────────────────────────────
ALTER TABLE tenant_tool_policies ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tenant_tool_policies ON public.tenant_tool_policies;
CREATE POLICY tenant_isolation_tenant_tool_policies ON public.tenant_tool_policies
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

ALTER TABLE tool_call_events ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tool_call_events ON public.tool_call_events;
CREATE POLICY tenant_isolation_tool_call_events ON public.tool_call_events
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

ALTER TABLE tool_usage_stats ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tool_usage_stats ON public.tool_usage_stats;
CREATE POLICY tenant_isolation_tool_usage_stats ON public.tool_usage_stats
    USING ((tenant_id)::text = (public.get_current_tenant())::text);