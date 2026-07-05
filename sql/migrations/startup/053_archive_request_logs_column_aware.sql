-- Migration 053: Fix archive_request_logs column-order mismatch
--
-- Problem:
--   request_logs and request_logs_archive have DIFFERENT column orders.
--   The original archive_request_logs() used `INSERT INTO ... SELECT *`,
--   which relies on positional column mapping. This fails when the column
--   orders differ (e.g., stream_interrupted(b) at position 32 in source
--   vs stream_chunks_sent(i) at position 32 in target).
--
-- Fix:
--   Build an explicit column list dynamically from information_schema,
--   then use `INSERT INTO (col1, col2, ...) SELECT col1, col2, ... FROM ...`.
--   This is safe regardless of column order in source vs target.
--
-- Discovered:
--   2026-06-26 in local Citus environment (citusdata/citus:11.3.0).
--   Verified that 184 production has the same column-order mismatch
--   (request_logs_archive columns 32-37 differ from request_logs columns 32-37).
--
-- Risk:
--   - 184 production has 0 archive_request_logs_* partitions (never invoked)
--   - Function definition change is metadata-only (no table rewrite)
--   - 71 replica will auto-sync via streaming replication
--   - Safe to apply during low-traffic period; recommended window:
--     llm-gateway-go PartitionManager archive cycle is daily at 03:00 UTC.

-- Drop old function (in case it has different signature)
DROP FUNCTION IF EXISTS archive_request_logs(date);

-- Recreate with explicit column mapping
CREATE OR REPLACE FUNCTION archive_request_logs(archive_month date)
RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
LANGUAGE plpgsql AS $func$
DECLARE
    month_start date := date_trunc('month', archive_month)::date;
    month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
    src_part    text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
    dst_part    text := 'request_logs_archive_' || to_char(month_start, 'YYYY_MM');
    row_count   bigint;
    partition_existed boolean := false;
    col_list    text;
BEGIN
    -- Check if source partition exists
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = src_part AND relnamespace = 'public'::regnamespace) THEN
        RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
        RETURN;
    END IF;

    partition_existed := true;

    -- Create archive partition if not exists (with columnar storage)
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = dst_part AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
            dst_part, month_start, month_end
        );
        RAISE NOTICE 'Created archive partition: %', dst_part;
    END IF;

    -- Build explicit column list: only columns that exist in BOTH source and archive
    -- (archive's ordinal order; intersection of source and archive columns)
    SELECT string_agg(a.column_name, ', ' ORDER BY a.ordinal_position)
    INTO col_list
    FROM information_schema.columns a
    JOIN information_schema.columns r
      ON a.table_schema = r.table_schema
     AND a.column_name  = r.column_name
    WHERE a.table_name = 'request_logs_archive'
      AND r.table_name = src_part
      AND a.table_schema = 'public'
      AND a.ordinal_position > 0;

    IF col_list IS NULL OR length(col_list) = 0 THEN
        RAISE EXCEPTION 'No common columns between % and request_logs_archive', src_part;
    END IF;

    -- Migrate data using explicit column list (safe even if column orders differ)
    EXECUTE format(
        'INSERT INTO %I (%s) SELECT %s FROM %I',
        dst_part, col_list, col_list, src_part
    );
    GET DIAGNOSTICS row_count = ROW_COUNT;
    RAISE NOTICE 'Migrated % rows from % to % (column-aware)', row_count, src_part, dst_part;

    -- Drop source partition (releases space)
    EXECUTE format('ALTER TABLE request_logs DETACH PARTITION %I', src_part);
    EXECUTE format('DROP TABLE %I', src_part);
    RAISE NOTICE 'Dropped source partition: %', src_part;

    RETURN QUERY SELECT 'success'::text, row_count, partition_existed;
END;
$func$;

COMMENT ON FUNCTION archive_request_logs(date) IS
'Archive one month of request_logs into request_logs_archive (columnar). '
'Column-aware: uses explicit column list, robust against column-order '
'differences between source and target partitions. '
'Added 2026-06-26 to fix INSERT ... SELECT * bug (local-discovery).';