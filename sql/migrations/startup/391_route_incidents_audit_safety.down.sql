-- 391_route_incidents_audit_safety.down.sql
-- Reverses the safety migration by dropping added columns and tables.

BEGIN;

-- Drop diagnostic_runs (was not in 252 before this migration)
DROP TABLE IF EXISTS diagnostic_runs CASCADE;

-- Drop approval_routing_rules (was not in 252 before this migration)
DROP TABLE IF EXISTS approval_routing_rules CASCADE;

-- Drop indices added
DROP INDEX IF EXISTS idx_routing_audit_log_idempotency;
DROP INDEX IF EXISTS idx_routing_audit_log_tenant_ts;
DROP INDEX IF EXISTS idx_routing_audit_log_incident;
DROP INDEX IF EXISTS idx_routing_audit_log_action;
DROP INDEX IF EXISTS idx_diagnostic_runs_tenant_started;
DROP INDEX IF EXISTS idx_diagnostic_runs_incident;
DROP INDEX IF EXISTS idx_diagnostic_runs_state;
DROP INDEX IF EXISTS idx_approval_routing_rules_tenant;

-- Drop CHECK constraint (best-effort)
ALTER TABLE routing_audit_log DROP CONSTRAINT IF EXISTS routing_audit_log_action_check;

-- Drop columns (data preserved in before_json/after_json)
ALTER TABLE routing_audit_log
    DROP COLUMN IF EXISTS incident_id,
    DROP COLUMN IF EXISTS tenant_id,
    DROP COLUMN IF EXISTS confirmation_token_hash,
    DROP COLUMN IF EXISTS idempotency_key,
    DROP COLUMN IF EXISTS request_payload,
    DROP COLUMN IF EXISTS pre_snapshot,
    DROP COLUMN IF EXISTS post_snapshot,
    DROP COLUMN IF EXISTS response_payload,
    DROP COLUMN IF EXISTS outcome,
    DROP COLUMN IF EXISTS failure_reason,
    DROP COLUMN IF EXISTS diagnostic_run_id,
    DROP COLUMN IF EXISTS actor_ip_hash,
    DROP COLUMN IF EXISTS created_at;

-- Note: routing_audit_log original columns (id, ts, actor, action, target_type,
-- target_id, before_json, after_json) are preserved.

COMMIT;
