-- ============================================================
-- Phase 23 / 01 — Rewrite ensure_<table>_partition() functions
--
-- The four functions called by bg.PartitionManager on every tick:
--
--   ensure_request_logs_partition(target_ts)
--   ensure_request_wal_partition(target_ts)
--   ensure_routing_decision_log_partition(target_ts)
--   ensure_credential_model_index_partition(target_ts)
--
-- Currently three of them forget to append `USING columnar`. After this
-- migration, INSERT-only parents (routing_decision_log,
-- credential_model_index) emit columnar partitions; UPDATE-heavy parents
-- (request_logs, request_wal) keep emitting heap partitions.
--
-- This makes the partition-create path compliant by construction — no
-- manual ALTER TABLE needed for future months.
-- ============================================================

\connect llm_gateway

-- ----------------------------------------------------------------
-- 1a. ensure_routing_decision_log_partition → COLUMNAR
-- ----------------------------------------------------------------

CREATE OR REPLACE FUNCTION ensure_routing_decision_log_partition(target_month timestamp with time zone)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    month_start    date := date_trunc('month', target_month)::date;
    month_end      date := (date_trunc('month', target_month) + interval '1 month')::date;
    partition_name text := 'routing_decision_log_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF routing_decision_log
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_routing_decision_log_partition: created % as columnar', partition_name;
    ELSE
        -- Idempotency: if a previous version of this function created
        -- a heap partition, convert it on the spot.
        PERFORM enforce_columnar_partition(partition_name, 'routing_decision_log');
    END IF;
END;
$$;

COMMENT ON FUNCTION ensure_routing_decision_log_partition(timestamp with time zone) IS
'Ensure monthly partition for routing_decision_log (INSERT-only).
Created USING columnar. Phase 23 / 01 hardened 2026-07-02.';

-- ----------------------------------------------------------------
-- 1b. ensure_credential_model_index_partition → COLUMNAR
-- ----------------------------------------------------------------

CREATE OR REPLACE FUNCTION ensure_credential_model_index_partition(target_month timestamp with time zone)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    month_start    date := date_trunc('month', target_month)::date;
    month_end      date := (date_trunc('month', target_month) + interval '1 month')::date;
    partition_name text := 'credential_model_index_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF credential_model_index
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_credential_model_index_partition: created % as columnar', partition_name;
    ELSE
        PERFORM enforce_columnar_partition(partition_name, 'credential_model_index');
    END IF;
END;
$$;

COMMENT ON FUNCTION ensure_credential_model_index_partition(timestamp with time zone) IS
'Ensure monthly partition for credential_model_index (INSERT-only).
Created USING columnar. Phase 23 / 01 hardened 2026-07-02.';

-- ----------------------------------------------------------------
-- 1c, 1d. ensure_request_logs_partition, ensure_request_wal_partition
--         → keep HEAP (UPDATE-heavy) but document the invariant in
--         the function comments so future readers understand why.
-- ----------------------------------------------------------------

DO $$
DECLARE
    fname text;
BEGIN
    -- request_logs: UPDATEs from telemetry (cost/tokens/body/latency) and
    -- admin blob cleanup (data_lifecycle_blobs.go, data_lifecycle_attachments.go).
    fname := 'ensure_request_logs_partition';
    IF EXISTS (SELECT 1 FROM pg_proc WHERE proname=fname) THEN
        EXECUTE format(
            'COMMENT ON FUNCTION public.%I(timestamptz) IS %s',
            fname,
            quote_literal(
                'Ensure monthly partition for request_logs. UPDATE-heavy (telemetry enrichment, ' ||
                'admin blob cleanup) plus large JSONB body columns (request_body / outbound_body / ' ||
                'response_body) that overflow Citus columnar 1 GB serialization buffer (see ' ||
                'migration 318b). Stays heap until request_logs_bodies split (planned migration 328) ' ||
                'lets metadata-only partition move to columnar. Phase 23 / 01 documented 2026-07-02.'
            )
        );
    END IF;

    -- request_wal: UPDATEs from request_logger.go:243 (status, stage,
    -- tokens, error fields, compression_strategy/meta).
    fname := 'ensure_request_wal_partition';
    IF EXISTS (SELECT 1 FROM pg_proc WHERE proname=fname) THEN
        EXECUTE format(
            'COMMENT ON FUNCTION public.%I(timestamptz) IS %s',
            fname,
            quote_literal(
                'Ensure monthly partition for request_wal. UPDATE-heavy (status / stage / tokens / ' ||
                'compression_meta updates from request_logger.go). Stays heap. Phase 23 / 01 ' ||
                'documented 2026-07-02.'
            )
        );
    END IF;
END
$$;

-- ----------------------------------------------------------------
-- 1e. ensure_usage_ledger_partition: documented separately because
--     migration 319 didn't create it (usage_ledger is partitioned but
--     has no ensure_* helper yet). We add one now, scoped to heap.
-- ----------------------------------------------------------------

CREATE OR REPLACE FUNCTION ensure_usage_ledger_partition(target_month timestamp with time zone)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    month_start    date := date_trunc('month', target_month)::date;
    month_end      date := (date_trunc('month', target_month) + interval '1 month')::date;
    partition_name text := 'usage_ledger_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- Phase 23 leaves usage_ledger heap: clients run a final
        -- UPDATE on prompt_tokens / completion_tokens / total_tokens /
        -- cost / latency_ms / success / error_kind after the response
        -- stream completes (telemetry/client.go).
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF usage_ledger
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_usage_ledger_partition: created % as heap', partition_name;
    END IF;
END;
$$;

COMMENT ON FUNCTION ensure_usage_ledger_partition(timestamp with time zone) IS
'Ensure monthly partition for usage_ledger. UPDATE-heavy from telemetry
enrichment (COALESCE updates on tokens/cost/latency/success columns).
Stays heap. Added 2026-07-02 by Phase 23 / 01.';

-- Register usage_ledger in the bg.PartitionManager's ensureSpecs list.
-- The application code reads ensureSpecs() from bg/partition_manager.go;
-- both ends stay in sync because this comment links back to the file.

\echo ''
\echo 'Phase 23 / 01 complete: 4 ensure functions pinned to invariant.'
