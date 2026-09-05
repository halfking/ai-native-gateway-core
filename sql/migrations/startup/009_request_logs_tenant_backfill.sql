-- Migration 009: tenant/task index for session list queries
-- Idempotent: safe to run multiple times.
-- Note: tenant_id backfill (from api_keys) is optional and may be run offline
-- on large tables; new rows already write tenant_id at insert time.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_schema = 'public' 
        AND table_name = 'request_logs' 
        AND column_name = 'gw_task_id'
    ) THEN
        CREATE INDEX IF NOT EXISTS idx_request_logs_tenant_task_ts
            ON request_logs (tenant_id, gw_task_id, ts DESC)
            WHERE gw_task_id IS NOT NULL AND gw_task_id <> '';
    END IF;
END $$;
