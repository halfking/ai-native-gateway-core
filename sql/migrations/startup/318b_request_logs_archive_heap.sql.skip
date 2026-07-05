-- Migration 318b: Switch request_logs_archive to heap storage
--
-- Background:
--   Migration 318 introduced chunked INSERTs into columnar archive
--   partitions. That fix works for the small/medium tables
--   (request_wal, routing_decision_log, credential_model_index) but
--   request_logs carries large JSONB columns (request_body,
--   response_body, outbound_body) — single rows can deserialize to
--   tens of MB and exhaust the 1 GB columnar string buffer.
--
--   The Citus columnar 11.3 writer flushes one row at a time through
--   a StringInfo buffer; with large JSONB payloads, the buffer
--   reaches the 1 GB cap and aborts with
--   "Cannot enlarge string buffer containing 1073387324 bytes".
--
-- Decision:
--   Keep request_logs_archive on heap (no columnar) for now. The other
--   three archive tables (request_wal_archive, routing_decision_log_archive,
--   credential_model_index_archive) continue to use columnar — their row
--   payloads are small and they fit comfortably in the columnar buffer.
--
--   The trade-off: request_logs_archive loses the ~10x columnar
--   compression on the 30 MB/partition we currently carry. A future
--   migration can split the large JSONB columns into a sibling
--   `request_logs_bodies` heap table, then convert the metadata-only
--   `request_logs_archive` to columnar.
--
-- This migration:
--   1) Drops any existing 2026_06 columnar partition (idempotent)
--   2) Recreates the partition as a plain heap
--   3) Updates archive_request_logs() so future calls also create heap
--      partitions instead of columnar ones.

-- Drop any existing 2026_06 columnar partition so we can re-create it as
-- a heap partition. If it does not exist (first run on 2026_06) this is
-- a no-op.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class
               WHERE relname = 'request_logs_archive_2026_06'
                 AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE 'DROP TABLE public.request_logs_archive_2026_06';
        RAISE NOTICE 'Dropped existing columnar partition request_logs_archive_2026_06';
    END IF;
END $$;

-- Re-create archive_request_logs: same logic, but build the destination
-- partition as a regular heap (no USING columnar).
DROP FUNCTION IF EXISTS archive_request_logs(date);

CREATE OR REPLACE FUNCTION archive_request_logs(archive_month date)
RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
LANGUAGE plpgsql AS $func$
DECLARE
    month_start date := date_trunc('month', archive_month)::date;
    month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
    src_part    text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
    dst_part    text := 'request_logs_archive_' || to_char(month_start, 'YYYY_MM');
    row_count   bigint := 0;
    chunk_count bigint := 0;
    col_list    text;
    last_id     bigint;
    batch_rows  bigint;
    CHUNK_SIZE  constant int := 1000;
BEGIN
    -- Check if source partition exists
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = src_part
                     AND relnamespace = 'public'::regnamespace) THEN
        RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
        RETURN;
    END IF;

    -- Create archive partition if not exists.
    -- NOTE: heap (not columnar). request_logs carries large JSONB
    -- columns that overflow Citus columnar's 1 GB string buffer on
    -- serialization. Migration 318b explains the trade-off and
    -- outlines a future body-table split.
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = dst_part
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_archive
             FOR VALUES FROM (%L) TO (%L)',
            dst_part, month_start, month_end
        );
    END IF;

    -- Build explicit column list (only columns common to source and archive)
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

    -- Chunked INSERT walked by id. Heap has no in-memory buffer
    -- cap, so the chunk size here is purely a checkpoint granularity
    -- — 1000 rows is a good balance between progress visibility
    -- and per-call overhead.
    last_id := -1;
    LOOP
        EXECUTE format(
            'INSERT INTO %I (%s) SELECT %s FROM %I
             WHERE id >  %L
             ORDER BY id
             LIMIT  %L',
            dst_part, col_list, col_list, src_part, last_id, CHUNK_SIZE
        );
        GET DIAGNOSTICS batch_rows = ROW_COUNT;
        EXIT WHEN batch_rows = 0;

        row_count   := row_count + batch_rows;
        chunk_count := chunk_count + 1;

        EXECUTE format(
            'SELECT MAX(id) FROM (SELECT id FROM %I WHERE id > %L ORDER BY id LIMIT %L) s',
            src_part, last_id, CHUNK_SIZE
        ) INTO last_id;
    END LOOP;

    RAISE NOTICE 'Migrated % rows from % to % in % chunks (chunk size %, heap storage)',
        row_count, src_part, dst_part, chunk_count, CHUNK_SIZE;

    -- Drop source partition (releases space)
    EXECUTE format('ALTER TABLE request_logs DETACH PARTITION %I', src_part);
    EXECUTE format('DROP TABLE %I', src_part);

    RETURN QUERY SELECT 'success'::text, row_count, true;
END;
$func$;

COMMENT ON FUNCTION archive_request_logs(date) IS
'Archive one month of request_logs into request_logs_archive (heap).
Column-aware: explicit column list, robust against column-order
differences. Chunked INSERT (1000 rows/iter) is a checkpoint
granularity — heap has no in-memory buffer cap.
request_logs_archive is intentionally NOT columnar: the table carries
large JSONB columns (request_body, response_body, outbound_body) that
overflow Citus columnar''s 1 GB string buffer on serialization. See
migration 318b for the full rationale and the future body-table split
plan. Idempotent in practice because the source partition is dropped
after a successful run.';

DO $$ BEGIN RAISE NOTICE 'Migration 318b completed: request_logs_archive switched to heap'; END $$;
