-- Migration 473 down: drop 2026_09 + 2026_10 partitions + default partitions
--
-- Note: this drops the partitions created by 473. The parent tables (and their
-- existing 2026_07 / 2026_08 partitions) remain untouched. To restore, re-run
-- 473_partition_precreate_2026_09_10.sql.

BEGIN;

DO $$
DECLARE
  parent_name text;
  targets text[] := ARRAY[
    'credential_model_index', 'credit_ledger', 'dashboard_access_events',
    'model_probe_runs', 'request_logs', 'request_wal',
    'routing_decision_log', 'routing_decision_log_archive',
    'session_bodies', 'session_module_executions', 'session_turns',
    'sessions', 'tool_usage_stats', 'usage_ledger'
  ];
BEGIN
  FOREACH parent_name IN ARRAY targets LOOP
    EXECUTE format('DROP TABLE IF EXISTS public.%I CASCADE', parent_name || '_2026_10');
    EXECUTE format('DROP TABLE IF EXISTS public.%I CASCADE', parent_name || '_2026_09');
    EXECUTE format('DROP TABLE IF EXISTS public.%I CASCADE', parent_name || '_default');
  END LOOP;
END $$;

COMMIT;