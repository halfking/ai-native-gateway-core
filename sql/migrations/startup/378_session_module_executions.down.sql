-- 378_session_module_executions.down.sql
--
-- Destructive rollback is intentionally unsupported. Migration 378 only removes
-- an unsafe RLS configuration and adds an index/check constraints. Restoring RLS
-- requires an application release that sets app.current_tenant for every pooled
-- writer; dropping the archived/hot tables would discard operational data.

DO $rollback$
BEGIN
    RAISE EXCEPTION
        'migration 378 has no automatic down migration: it protects asynchronous module execution writes; restore only with a reviewed application/database rollback plan';
END
$rollback$;
