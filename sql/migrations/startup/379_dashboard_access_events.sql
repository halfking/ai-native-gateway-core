-- 379_dashboard_access_events.sql
--
-- Compatibility migration for databases where 361 was already applied. It does
-- not recreate or replace the published schema. Telemetry inserts asynchronously
-- through a pooled connection and does not set app.current_tenant, so RLS on these
-- hot/archive tables would reject valid operational writes.

BEGIN;

DO $migration$
BEGIN
    IF to_regclass('public.dashboard_access_events_hot') IS NULL
       OR to_regclass('public.dashboard_access_events') IS NULL THEN
        RAISE EXCEPTION
            'migration 379 requires the tables created by migration 361; apply 361 first';
    END IF;
END
$migration$;

-- Remove only policies introduced by the withdrawn replacement migration.
DROP POLICY IF EXISTS tenant_isolation_dashboard_access_events_hot
    ON public.dashboard_access_events_hot;
DROP POLICY IF EXISTS tenant_isolation_dashboard_access_events
    ON public.dashboard_access_events;

-- Do not enable RLS here. Telemetry uses pooled asynchronous writes without a
-- transaction-scoped app.current_tenant value required by a tenant policy.
ALTER TABLE public.dashboard_access_events_hot DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.dashboard_access_events DISABLE ROW LEVEL SECURITY;

-- Protect all new writes without scanning or rewriting historical event rows.
ALTER TABLE public.dashboard_access_events_hot
    DROP CONSTRAINT IF EXISTS chk_dae_hot_event_type;
ALTER TABLE public.dashboard_access_events_hot
    ADD CONSTRAINT chk_dae_hot_event_type
    CHECK (event_type IN ('api_access', 'query', 'export', 'error')) NOT VALID;

ALTER TABLE public.dashboard_access_events
    DROP CONSTRAINT IF EXISTS chk_dae_event_type;
ALTER TABLE public.dashboard_access_events
    ADD CONSTRAINT chk_dae_event_type
    CHECK (event_type IN ('api_access', 'query', 'export', 'error')) NOT VALID;

CREATE INDEX IF NOT EXISTS idx_dae_hot_cleanup
    ON public.dashboard_access_events_hot (created_at);

COMMIT;
