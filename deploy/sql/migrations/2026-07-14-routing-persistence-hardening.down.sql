-- Rollback for 2026-07-14-routing-persistence-hardening.
--
-- This rollback removes only objects created exclusively by the hardening
-- migration. It deliberately preserves additive columns and existing data;
-- dropping those columns would destroy telemetry already written by newer
-- gateway binaries. Disable the application or use a database snapshot before
-- any destructive rollback.

BEGIN;

DROP INDEX IF EXISTS idx_approval_routing_rules_tenant;
DROP TABLE IF EXISTS public.approval_routing_rules;

DROP INDEX IF EXISTS idx_diagnostic_runs_state;
DROP INDEX IF EXISTS idx_diagnostic_runs_incident;
DROP INDEX IF EXISTS idx_diagnostic_runs_created;
DROP INDEX IF EXISTS idx_diagnostic_runs_tenant_started;
DROP TABLE IF EXISTS public.diagnostic_runs;

DROP INDEX IF EXISTS idx_routing_audit_log_action;
DROP INDEX IF EXISTS idx_routing_audit_log_incident;
DROP INDEX IF EXISTS idx_routing_audit_log_tenant_ts;
DROP INDEX IF EXISTS idx_routing_audit_log_idempotency;

DROP INDEX IF EXISTS idx_routing_decision_log_hot_request_ts;
DROP INDEX IF EXISTS idx_routing_decision_log_hot_tenant_ts;
DROP INDEX IF EXISTS idx_routing_decision_log_hot_ts;
DROP INDEX IF EXISTS idx_request_wal_hot_tenant_created;
DROP INDEX IF EXISTS idx_request_wal_hot_created_at;
DROP INDEX IF EXISTS idx_request_logs_hot_multimodal_usage;
DROP INDEX IF EXISTS idx_request_logs_hot_tenant_ts;
DROP INDEX IF EXISTS idx_request_logs_hot_ts;

DO $$
BEGIN
    IF to_regclass('public.schema_migrations') IS NOT NULL THEN
        DELETE FROM public.schema_migrations
         WHERE version = '2026-07-14-routing-persistence-hardening';
    END IF;
END $$;

COMMIT;
