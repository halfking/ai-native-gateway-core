-- Rollback for 649_routing_analytics_probe_filter.sql.
-- The views are derived data and are recreated by migration 632/ensure logic.
BEGIN;
DROP MATERIALIZED VIEW IF EXISTS public.routing_analytics_7d CASCADE;
DROP MATERIALIZED VIEW IF EXISTS public.routing_audit_summary_7d CASCADE;
DROP VIEW IF EXISTS public.routing_analytics_source;
COMMIT;
