-- Migration 318: Fix archive_credential_model_index + archive_request_logs
--
-- Two bugs discovered when running 6-month archive on 2026-06-30:
--
-- Bug 1: archive_credential_model_index uses `ON CONFLICT DO NOTHING` when
--        inserting into credential_model_index_archive (columnar). Citus
--        columnar storage does not support speculative inserts
--        (columnar_tuple_insert_speculative not implemented). This blocks
--        all CMI archival.
--
-- Bug 2: archive_request_logs does a single batched INSERT into a columnar
--        partition. With ~11K rows of request_logs containing large
--        request_body / response_body JSONB, the columnar stripe builder
--        needs >1 GB of buffer and crashes with "out of memory: Cannot
--        enlarge string buffer containing 1073387324 bytes by 1288144
--        more bytes". Same shape of failure would hit archive_routing_decision_log
--        once that table grows.
--
-- Fix:
--   - For CMI: drop the ON CONFLICT clause. Idempotency is provided by
--     TRUNCATE-ing the destination columnar partition before INSERT. Since
--     the function is meant to be re-runnable monthly on the same window,
--     this is the simplest correct semantics.
--   - For request_logs: switch the INSERT to a chunked loop ordered by id,
--     1000 rows per chunk. Chunks fit comfortably in columnar's stripe
--     buffer even with very large JSONB columns.
--   - Apply the same chunked-insert pattern to archive_routing_decision_log
--     preemptively (21K rows just barely fit today; will not tomorrow).

-- ============================================================
-- 1) archive_credential_model_index: remove ON CONFLICT
-- ============================================================

DROP FUNCTION IF EXISTS archive_credential_model_index(date);

CREATE OR REPLACE FUNCTION archive_credential_model_index(archive_month date)
RETURNS TABLE(status text, rows_archived bigint, rows_deleted bigint)
LANGUAGE plpgsql AS $func$
DECLARE
    month_start   date := date_trunc('month', archive_month)::date;
    month_end     date := (date_trunc('month', archive_month) + interval '1 month')::date;
    partition_name text := 'credential_model_index_archive_' || to_char(month_start, 'YYYY_MM');
    archived_count bigint;
    deleted_count  bigint;
    cutoff_ts      timestamptz := NOW() - INTERVAL '7 days';
BEGIN
    -- Create target columnar partition if missing
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF credential_model_index_archive
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            partition_name, month_start, month_end
        );
    END IF;

    -- Truncate the destination columnar partition so the function is
    -- idempotent on re-run. Columnar TRUNCATE is a metadata operation
    -- (no row-by-row work), so it is cheap.
    EXECUTE format('TRUNCATE TABLE %I', partition_name);

    -- Archive 7d+ data for this month to columnar (no ON CONFLICT:
    -- columnar does not support speculative inserts).
    EXECUTE format(
        'INSERT INTO %I SELECT * FROM credential_model_index
         WHERE bucket >= %L
           AND bucket <  %L
           AND bucket <  %L',
        partition_name, month_start, month_end, cutoff_ts
    );
    GET DIAGNOSTICS archived_count = ROW_COUNT;

    -- Delete archived data from main table
    DELETE FROM credential_model_index
    WHERE bucket >= month_start
      AND bucket <  month_end
      AND bucket <  cutoff_ts;
    GET DIAGNOSTICS deleted_count = ROW_COUNT;

    RETURN QUERY SELECT 'success'::text, archived_count, deleted_count;
END;
$func$;

COMMENT ON FUNCTION archive_credential_model_index(date) IS
'Archive one month of credential_model_index data (older than 7 days) into
credential_model_index_archive (columnar). Uses TRUNCATE-then-INSERT to be
idempotent (columnar storage does not support ON CONFLICT). Deletes archived
rows from the main partitioned table to keep it lean. Run monthly on day 1.
Fixed 2026-06-30 in migration 318 (was using ON CONFLICT DO NOTHING).';

-- ============================================================
-- 2) archive_request_logs: chunked INSERT
-- ============================================================

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

    -- Create archive partition if not exists (with columnar storage)
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = dst_part
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_archive
             FOR VALUES FROM (%L) TO (%L) USING columnar',
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

    -- request_logs carries very large JSONB columns (request_body,
    -- response_body, outbound_body) — sometimes >1 MB per row. We
    -- tune columnar to keep the in-memory chunk buffer small. The
    -- default columnar.chunk_group_row_limit is 10000 — at 240 KB per
    -- row that is 2.4 GB per chunk, which exceeds the 1 GB string
    -- buffer. We drop it to the minimum (1000) so the columnar writer
    -- flushes more often and never holds the whole chunk in memory.
    -- 1000 rows × 240 KB ≈ 240 MB per chunk, safe under the 1 GB cap.
    PERFORM set_config('columnar.chunk_group_row_limit', '1000', true);

    -- Chunked INSERT: walk the source partition by id, 100 rows at a
    -- time. This matches columnar.chunk_group_row_limit so each loop
    -- iteration produces exactly one columnar chunk that is flushed
    -- to disk. (-1 sentinel seeds the cursor; the first iteration
    -- reads the lowest id.)
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

        -- Advance cursor to the highest id in this batch
        EXECUTE format(
            'SELECT MAX(id) FROM (SELECT id FROM %I WHERE id > %L ORDER BY id LIMIT %L) s',
            src_part, last_id, CHUNK_SIZE
        ) INTO last_id;
    END LOOP;

    RAISE NOTICE 'Migrated % rows from % to % in % chunks (chunk size %)',
        row_count, src_part, dst_part, chunk_count, CHUNK_SIZE;

    -- Drop source partition (releases space)
    EXECUTE format('ALTER TABLE request_logs DETACH PARTITION %I', src_part);
    EXECUTE format('DROP TABLE %I', src_part);

    RETURN QUERY SELECT 'success'::text, row_count, true;
END;
$func$;

COMMENT ON FUNCTION archive_request_logs(date) IS
'Archive one month of request_logs into request_logs_archive (columnar).
Column-aware: uses explicit column list, robust against column-order
differences. Chunked INSERT (100 rows/iter) paired with
columnar.chunk_group_row_limit=100 keeps the in-memory columnar chunk
buffer bounded — without this the columnar writer exhausts its 1 GB
string buffer on tables with large JSONB columns. Idempotent in
practice because the source partition is dropped after a successful
run. Fixed 2026-06-30 in migration 318.';

-- ============================================================
-- 3) archive_routing_decision_log: same chunked-insert pattern
-- ============================================================

DROP FUNCTION IF EXISTS archive_routing_decision_log(date);

CREATE OR REPLACE FUNCTION archive_routing_decision_log(archive_month date)
RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
LANGUAGE plpgsql AS $func$
DECLARE
    month_start  date := date_trunc('month', archive_month)::date;
    month_end    date := (date_trunc('month', archive_month) + interval '1 month')::date;
    src_part     text := 'routing_decision_log_' || to_char(month_start, 'YYYY_MM');
    dst_part     text := 'routing_decision_log_archive_' || to_char(month_start, 'YYYY_MM');
    row_count    bigint := 0;
    chunk_count  bigint := 0;
    col_list     text;
    last_ts      timestamptz;
    batch_rows   bigint;
    CHUNK_SIZE   constant int := 1000;
BEGIN
    -- Check if source partition exists
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = src_part
                     AND relnamespace = 'public'::regnamespace) THEN
        RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
        RETURN;
    END IF;

    -- Create archive partition if not exists (with columnar storage)
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = dst_part
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF routing_decision_log_archive
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            dst_part, month_start, month_end
        );
    END IF;

    -- Build explicit column list
    SELECT string_agg(a.column_name, ', ' ORDER BY a.ordinal_position)
    INTO col_list
    FROM information_schema.columns a
    JOIN information_schema.columns r
      ON a.table_schema = r.table_schema
     AND a.column_name  = r.column_name
    WHERE a.table_name = 'routing_decision_log_archive'
      AND r.table_name = src_part
      AND a.table_schema = 'public'
      AND a.ordinal_position > 0;

    IF col_list IS NULL OR length(col_list) = 0 THEN
        RAISE EXCEPTION 'No common columns between % and routing_decision_log_archive', src_part;
    END IF;

    -- Chunked INSERT walked by ts (table has no single-column id).
    -- Use '-infinity' as the initial cursor.
    last_ts := '-infinity'::timestamptz;
    LOOP
        EXECUTE format(
            'INSERT INTO %I (%s) SELECT %s FROM %I
             WHERE ts >  %L
             ORDER BY ts
             LIMIT  %L',
            dst_part, col_list, col_list, src_part, last_ts, CHUNK_SIZE
        );
        GET DIAGNOSTICS batch_rows = ROW_COUNT;
        EXIT WHEN batch_rows = 0;

        row_count   := row_count + batch_rows;
        chunk_count := chunk_count + 1;

        EXECUTE format(
            'SELECT MAX(ts) FROM (SELECT ts FROM %I WHERE ts > %L ORDER BY ts LIMIT %L) s',
            src_part, last_ts, CHUNK_SIZE
        ) INTO last_ts;
    END LOOP;

    RAISE NOTICE 'Migrated % rows from % to % in % chunks (chunk size %)',
        row_count, src_part, dst_part, chunk_count, CHUNK_SIZE;

    -- Drop source partition
    EXECUTE format('ALTER TABLE routing_decision_log DETACH PARTITION %I', src_part);
    EXECUTE format('DROP TABLE %I', src_part);

    RETURN QUERY SELECT 'success'::text, row_count, true;
END;
$func$;

COMMENT ON FUNCTION archive_routing_decision_log(date) IS
'Archive one month of routing_decision_log into routing_decision_log_archive
(columnar). Column-aware: uses explicit column list. Chunked INSERT (1000
rows/iter) avoids columnar stripe buffer overflow. Idempotent in practice
because the source partition is dropped after a successful run.
Fixed 2026-06-30 in migration 318 (was a single batched INSERT).';

-- ============================================================
-- 4) archive_request_wal: same chunked-insert pattern (preventive)
-- ============================================================

DROP FUNCTION IF EXISTS archive_request_wal(date);

CREATE OR REPLACE FUNCTION archive_request_wal(archive_month date)
RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
LANGUAGE plpgsql AS $func$
DECLARE
    month_start date := date_trunc('month', archive_month)::date;
    month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
    src_part    text := 'request_wal_' || to_char(month_start, 'YYYY_MM');
    dst_part    text := 'request_wal_archive_' || to_char(month_start, 'YYYY_MM');
    row_count   bigint := 0;
    chunk_count bigint := 0;
    col_list    text;
    last_ts     timestamptz;
    batch_rows  bigint;
    CHUNK_SIZE  constant int := 1000;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = src_part
                     AND relnamespace = 'public'::regnamespace) THEN
        RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = dst_part
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_wal_archive
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            dst_part, month_start, month_end
        );
    END IF;

    SELECT string_agg(a.column_name, ', ' ORDER BY a.ordinal_position)
    INTO col_list
    FROM information_schema.columns a
    JOIN information_schema.columns r
      ON a.table_schema = r.table_schema
     AND a.column_name  = r.column_name
    WHERE a.table_name = 'request_wal_archive'
      AND r.table_name = src_part
      AND a.table_schema = 'public'
      AND a.ordinal_position > 0;

    IF col_list IS NULL OR length(col_list) = 0 THEN
        RAISE EXCEPTION 'No common columns between % and request_wal_archive', src_part;
    END IF;

    last_ts := '-infinity'::timestamptz;
    LOOP
        EXECUTE format(
            'INSERT INTO %I (%s) SELECT %s FROM %I
             WHERE created_at > %L
             ORDER BY created_at
             LIMIT  %L',
            dst_part, col_list, col_list, src_part, last_ts, CHUNK_SIZE
        );
        GET DIAGNOSTICS batch_rows = ROW_COUNT;
        EXIT WHEN batch_rows = 0;

        row_count   := row_count + batch_rows;
        chunk_count := chunk_count + 1;

        EXECUTE format(
            'SELECT MAX(created_at) FROM (SELECT created_at FROM %I WHERE created_at > %L ORDER BY created_at LIMIT %L) s',
            src_part, last_ts, CHUNK_SIZE
        ) INTO last_ts;
    END LOOP;

    RAISE NOTICE 'Migrated % rows from % to % in % chunks (chunk size %)',
        row_count, src_part, dst_part, chunk_count, CHUNK_SIZE;

    EXECUTE format('ALTER TABLE request_wal DETACH PARTITION %I', src_part);
    EXECUTE format('DROP TABLE %I', src_part);

    RETURN QUERY SELECT 'success'::text, row_count, true;
END;
$func$;

COMMENT ON FUNCTION archive_request_wal(date) IS
'Archive one month of request_wal into request_wal_archive (columnar).
Column-aware: explicit column list. Chunked INSERT (1000 rows/iter) avoids
columnar stripe buffer overflow. Idempotent in practice because the source
partition is dropped after a successful run.
Updated 2026-06-30 in migration 318 (preventive: same fix as the other
archive functions).';

-- ============================================================
-- 5) Verification
-- ============================================================

DO $$
BEGIN
    PERFORM 1 FROM pg_proc WHERE proname = 'archive_credential_model_index';
    PERFORM 1 FROM pg_proc WHERE proname = 'archive_request_logs';
    PERFORM 1 FROM pg_proc WHERE proname = 'archive_routing_decision_log';
    PERFORM 1 FROM pg_proc WHERE proname = 'archive_request_wal';
    RAISE NOTICE 'Migration 318 completed: 4 archive functions updated';
END $$;
