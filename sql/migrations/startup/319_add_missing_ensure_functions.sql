-- Migration 319: Add missing ensure_xxx_partition functions
--
-- Background:
--   bg/partition_manager.go calls ensure_request_logs_partition(ts),
--   ensure_request_wal_partition(ts), ensure_routing_decision_log_partition(ts),
--   and ensure_credential_model_index_partition(ts) on every tick.
--
--   Migration 305 added ensure_request_wal_partition().
--   But ensure_routing_decision_log_partition() and
--   ensure_credential_model_index_partition() were never created.
--
--   The DB has ensure_next_month_routing_archive_partition() with no args,
--   which is a different signature and not callable from the Go code.
--
-- This migration creates the two missing ensure functions with the standard
-- signature: (timestamp with time zone) RETURNS void. They create monthly
-- partitions idempotently.

-- ============================================================
-- 1. ensure_routing_decision_log_partition
-- ============================================================

CREATE OR REPLACE FUNCTION ensure_routing_decision_log_partition(target_month timestamp with time zone)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    month_start date := date_trunc('month', target_month)::date;
    month_end   date := (date_trunc('month', target_month) + interval '1 month')::date;
    partition_name text := 'routing_decision_log_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF routing_decision_log
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'Created partition % for routing_decision_log', partition_name;
    END IF;
END;
$$;

COMMENT ON FUNCTION ensure_routing_decision_log_partition(timestamp with time zone) IS
'Ensure a monthly partition exists for routing_decision_log at the given month.
Called by bg.PartitionManager on every tick for current + next month.
Idempotent. Added 2026-06-30 in migration 319.';

-- ============================================================
-- 2. ensure_credential_model_index_partition
-- ============================================================

CREATE OR REPLACE FUNCTION ensure_credential_model_index_partition(target_month timestamp with time zone)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    month_start date := date_trunc('month', target_month)::date;
    month_end   date := (date_trunc('month', target_month) + interval '1 month')::date;
    partition_name text := 'credential_model_index_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF credential_model_index
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'Created partition % for credential_model_index', partition_name;
    END IF;
END;
$$;

COMMENT ON FUNCTION ensure_credential_model_index_partition(timestamp with time zone) IS
'Ensure a monthly partition exists for credential_model_index at the given month.
Called by bg.PartitionManager on every tick for current + next month.
Idempotent. Added 2026-06-30 in migration 319.';

-- ============================================================
-- 3. Verification
-- ============================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'ensure_routing_decision_log_partition') THEN
        RAISE EXCEPTION 'ensure_routing_decision_log_partition not created';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'ensure_credential_model_index_partition') THEN
        RAISE EXCEPTION 'ensure_credential_model_index_partition not created';
    END IF;
    RAISE NOTICE 'Migration 319 completed: 2 ensure functions added';
END $$;
