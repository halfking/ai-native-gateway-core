-- Migration 009: backfill request_logs.tenant_id + tenant/task index
-- Idempotent: safe to run multiple times.

-- Backfill tenant_id from api_keys where row still has default/empty tenant.
UPDATE request_logs rl
SET tenant_id = ak.tenant_id
FROM api_keys ak
WHERE rl.api_key_id = ak.id
  AND ak.tenant_id IS NOT NULL
  AND ak.tenant_id <> ''
  AND (rl.tenant_id IS NULL OR rl.tenant_id = '' OR rl.tenant_id = 'default')
  AND ak.tenant_id <> 'default';

CREATE INDEX IF NOT EXISTS idx_request_logs_tenant_task_ts
    ON request_logs (tenant_id, gw_task_id, ts DESC)
    WHERE gw_task_id IS NOT NULL AND gw_task_id <> '';
