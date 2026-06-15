-- Migration 009: tenant/task index for session list queries
-- Idempotent: safe to run multiple times.
-- Note: tenant_id backfill (from api_keys) is optional and may be run offline
-- on large tables; new rows already write tenant_id at insert time.

CREATE INDEX IF NOT EXISTS idx_request_logs_tenant_task_ts
    ON request_logs (tenant_id, gw_task_id, ts DESC)
    WHERE gw_task_id IS NOT NULL AND gw_task_id <> '';
