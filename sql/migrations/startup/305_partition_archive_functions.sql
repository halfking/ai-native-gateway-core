-- Migration 305: Add partition archive functions for request_wal
--
-- Purpose:
--   Extend the partition management system to support archiving
--   request_wal partitions to columnar storage, similar to request_logs.
--
-- Background:
--   - request_logs already has archive_request_logs() function (migration 053)
--   - request_wal is also a partitioned table (by created_at)
--   - Both tables benefit from columnar archive for old data (2+ months)
--
-- Implementation:
--   1. Create request_wal_archive parent table (if not exists)
--   2. Create archive_request_wal() function
--   3. Add ensure_request_wal_partition() for consistency
--
-- Date: 2026-06-28

-- ============================================================
-- 1. Create request_wal_archive table (columnar parent)
-- ============================================================

-- Check if request_wal_archive already exists
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal_archive' AND relnamespace = 'public'::regnamespace) THEN
        -- Create archive parent table with same structure as request_wal
        CREATE TABLE request_wal_archive (
            LIKE request_wal INCLUDING DEFAULTS INCLUDING CONSTRAINTS
        ) PARTITION BY RANGE (created_at);
        
        -- Add comment
        COMMENT ON TABLE request_wal_archive IS 
            'Columnar archive for old request_wal partitions (2+ months old). '
            'Partitions are migrated via archive_request_wal() function.';
        
        RAISE NOTICE 'Created request_wal_archive parent table';
    ELSE
        RAISE NOTICE 'request_wal_archive table already exists, skipping';
    END IF;
END $$;

-- ============================================================
-- 2. Create archive_request_wal() function
-- ============================================================

CREATE OR REPLACE FUNCTION archive_request_wal(archive_month date)
RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
LANGUAGE plpgsql AS $func$
DECLARE
    month_start date := date_trunc('month', archive_month)::date;
    month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
    src_part    text := 'request_wal_' || to_char(month_start, 'YYYY_MM');
    dst_part    text := 'request_wal_archive_' || to_char(month_start, 'YYYY_MM');
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
            'CREATE TABLE %I PARTITION OF request_wal_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
            dst_part, month_start, month_end
        );
        RAISE NOTICE 'Created archive partition: %', dst_part;
    END IF;

    -- Build explicit column list: only columns that exist in BOTH source and archive
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

    -- Migrate data using explicit column list (safe even if column orders differ)
    EXECUTE format(
        'INSERT INTO %I (%s) SELECT %s FROM %I',
        dst_part, col_list, col_list, src_part
    );
    GET DIAGNOSTICS row_count = ROW_COUNT;
    RAISE NOTICE 'Migrated % rows from % to % (column-aware)', row_count, src_part, dst_part;

    -- Drop source partition (releases space)
    EXECUTE format('ALTER TABLE request_wal DETACH PARTITION %I', src_part);
    EXECUTE format('DROP TABLE %I', src_part);
    RAISE NOTICE 'Dropped source partition: %', src_part;

    RETURN QUERY SELECT 'success'::text, row_count, partition_existed;
END;
$func$;

COMMENT ON FUNCTION archive_request_wal(date) IS
'Archive one month of request_wal into request_wal_archive (columnar). '
'Column-aware: uses explicit column list, robust against column-order '
'differences between source and target partitions. '
'Added 2026-06-28 for data lifecycle management.';

-- ============================================================
-- 3. Create ensure_request_wal_partition() function
-- ============================================================

CREATE OR REPLACE FUNCTION ensure_request_wal_partition(target_ts timestamp with time zone DEFAULT now())
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    month_start   date := date_trunc('month', target_ts)::date;
    month_end     date := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name     text := 'request_wal_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_wal FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end
        );
        RAISE NOTICE 'Created partition: %', part_name;
    END IF;
END;
$$;

COMMENT ON FUNCTION ensure_request_wal_partition(timestamp with time zone) IS
'Ensure request_wal partition exists for the given timestamp. '
'Creates monthly partition if not exists. '
'Added 2026-06-28 for automated partition management.';

-- ============================================================
-- 4. Verification
-- ============================================================

-- Verify functions exist
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'archive_request_wal') THEN
        RAISE EXCEPTION 'archive_request_wal() function not created';
    END IF;
    
    IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'ensure_request_wal_partition') THEN
        RAISE EXCEPTION 'ensure_request_wal_partition() function not created';
    END IF;
    
    RAISE NOTICE 'Migration 305 completed successfully';
END $$;
