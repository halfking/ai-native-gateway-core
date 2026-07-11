-- 382_session_module_executions.down.sql
--
-- Destructive rollback is intentionally unsupported. This migration creates
-- operational tables and applies pooled-writer compatibility safeguards.

DO $rollback$
BEGIN
    RAISE EXCEPTION
        'migration 382 has no automatic down migration: dropping session module execution tables would discard operational data; restore only with a reviewed application/database rollback plan';
END
$rollback$;
