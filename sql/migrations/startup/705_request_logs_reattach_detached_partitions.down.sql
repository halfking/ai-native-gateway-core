-- 705 down: restore the 694 body of ensure_request_logs_partition.
--
-- The repaired DATA stays exactly as 705 left it: the monthly partitions
-- remain attached and request_logs_default remains drained — re-detaching
-- would reopen the promote 断链 (rows would pile into the default again),
-- so this down is an emergency rewind of the ROUTING LOGIC only, not of the
-- data layout. The 705 helper functions (sync_partition_columns /
-- repair_request_logs_detached_partitions) are intentionally kept: they have
-- no side effects unless called, and a re-apply of 705 needs them.
-- The schema_migrations ledger row is intentionally kept (append-only
-- ledger, mirrors 701/703/704 down convention of leaving stamps in place).

\set ON_ERROR_STOP on

BEGIN;
SELECT pg_advisory_xact_lock(hashtextextended('llm-gateway:request-logs-repair-705', 0));

CREATE OR REPLACE FUNCTION public.ensure_request_logs_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start   date;
    month_end     date;
    part_name     text;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name := 'request_logs_' || to_char(month_start, 'YYYY_MM');

    -- Pre-705 semantics (the 断链 bug): any table of this name in pg_class
    -- counts as "exists", including DETACHED shells, so ensure becomes a
    -- no-op and promote inserts keep falling into request_logs_default.
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end);
        EXECUTE format(
            'CREATE INDEX idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
            part_name, part_name
        );
        -- 2026-06-24 (migration 043): GIN trgm on client_model so the
        -- /api/logs ?model= ILIKE filter can use a bitmap index scan
        -- instead of a partition Seq Scan once volume grows.
        EXECUTE format(
            'CREATE INDEX idx_%s_client_model_trgm ON %I USING gin (client_model gin_trgm_ops)',
            part_name, part_name
        );
    END IF;
END;
$$;

COMMIT;
