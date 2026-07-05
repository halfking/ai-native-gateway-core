-- Migration 053 rollback: Restore SELECT * version (NOT recommended)
--
-- WARNING: This rollback restores the original buggy behavior where
-- `INSERT INTO ... SELECT *` is used. It will FAIL on 184 production
-- because request_logs and request_logs_archive have different column orders.
-- Only run this rollback if you understand the implications.

DROP FUNCTION IF EXISTS archive_request_logs(date);

CREATE OR REPLACE FUNCTION archive_request_logs(archive_month date)
RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
LANGUAGE plpgsql AS $$
DECLARE
    month_start date := date_trunc('month', archive_month)::date;
    month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
    src_part    text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
    dst_part    text := 'request_logs_archive_' || to_char(month_start, 'YYYY_MM');
    row_count   bigint;
    partition_existed boolean := false;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = src_part AND relnamespace = 'public'::regnamespace) THEN
        RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
        RETURN;
    END IF;

    partition_existed := true;

    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = dst_part AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
            dst_part, month_start, month_end
        );
    END IF;

    -- BUG: relies on column order matching between source and archive
    EXECUTE format('INSERT INTO %I SELECT * FROM %I', dst_part, src_part);
    GET DIAGNOSTICS row_count = ROW_COUNT;

    EXECUTE format('ALTER TABLE request_logs DETACH PARTITION %I', src_part);
    EXECUTE format('DROP TABLE %I', src_part);

    RETURN QUERY SELECT 'success'::text, row_count, partition_existed;
END;
$$;