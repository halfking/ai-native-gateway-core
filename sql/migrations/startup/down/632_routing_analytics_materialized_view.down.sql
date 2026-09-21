-- Migration 632 DOWN: Remove routing analytics materialized views
--
-- This rollback removes the performance optimization materialized views
-- and restores the original direct query behavior.

BEGIN;

DROP MATERIALIZED VIEW IF EXISTS routing_analytics_7d CASCADE;
DROP MATERIALIZED VIEW IF EXISTS routing_audit_summary_7d CASCADE;
-- DROP MATERIALIZED VIEW IF EXISTS routing_analytics_30d CASCADE;  -- if created later

COMMIT;
