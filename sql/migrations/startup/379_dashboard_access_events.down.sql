-- 379_dashboard_access_events.down.sql
--
-- Destructive rollback is intentionally unsupported. Migration 379 only removes
-- an unsafe RLS configuration and adds an index/check constraints. Restoring RLS
-- requires an application release that sets app.current_tenant for every pooled
-- telemetry writer; dropping the archived/hot tables would discard audit data.

DO $rollback$
BEGIN
    RAISE EXCEPTION
        'migration 379 has no automatic down migration: it protects asynchronous telemetry writes; restore only with a reviewed application/database rollback plan';
END
$rollback$;
