-- Migration 360: Drop redundant handoff_logs index
--
-- Problem: idx_handoff_logs_tenant and idx_handoff_logs_tenant_created
-- are identical (both: tenant_id, created_at DESC). Created in two
-- different migrations (idx_handoff_logs_tenant in the original schema,
-- idx_handoff_logs_tenant_created in migration 359_handoff_schema_fix.sql).
--
-- Keeping both doubles the write cost on every handoff_logs INSERT
-- and wastes ~MB-scale storage on a table whose TOAST payload is
-- already the dominant cost driver. Drop the redundant duplicate;
-- keep idx_handoff_logs_tenant (original, narrower name).

BEGIN;

DROP INDEX IF EXISTS idx_handoff_logs_tenant_created;

COMMIT;
