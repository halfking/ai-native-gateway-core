-- 383_dashboard_access_events.down.sql
--
-- Destructive rollback is intentionally unsupported. This migration creates
-- audit tables and applies pooled-writer compatibility safeguards.

DO $rollback$
BEGIN
    RAISE EXCEPTION
        'migration 383 has no automatic down migration: dropping dashboard access event tables would discard audit data; restore only with a reviewed application/database rollback plan';
END
$rollback$;
