-- Rollback: 450_request_stage_events_tenant

BEGIN;

DO $$
BEGIN
    IF to_regclass('public.request_stage_events') IS NOT NULL THEN
        DROP INDEX IF EXISTS public.idx_stage_events_tenant_ts;
        ALTER TABLE public.request_stage_events
            DROP COLUMN IF EXISTS tenant_id;
    END IF;
END $$;

COMMIT;
