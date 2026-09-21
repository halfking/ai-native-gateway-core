-- Rollback for 648_archive_credential_model_index_detach_drop.sql
--
-- Revert to the 653 function body (canonical return tuple, DELETE-from-partition
-- body). DELETE only works for heap monthly partitions — re-applying the
-- 653 body will reintroduce "UPDATE and CTID scans not supported for ColumnarScan"
-- errors for any month whose source partition is columnar (2026_07 / 2026_08
-- as of 2026-09-04).

\set ON_ERROR_STOP on

DROP FUNCTION IF EXISTS public.archive_credential_model_index(date);

CREATE OR REPLACE FUNCTION public.archive_credential_model_index(archive_month date)
    RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
    LANGUAGE plpgsql
    AS $$
        DECLARE
            month_start date := date_trunc('month', archive_month)::date;
            month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
            partition_name text := 'credential_model_index_archive_' || to_char(month_start, 'YYYY_MM');
            archived_count bigint;
            deleted_count bigint;
            cutoff_ts timestamptz := NOW() - INTERVAL '7 days';
        BEGIN
            IF NOT EXISTS (SELECT 1 FROM pg_class
                           WHERE relname = partition_name AND relnamespace = 'public'::regnamespace) THEN
                EXECUTE format(
                    'CREATE TABLE %I PARTITION OF credential_model_index_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
                    partition_name, month_start, month_end
                );
            END IF;

            INSERT INTO credential_model_index_archive
            SELECT * FROM credential_model_index
            WHERE bucket >= month_start
              AND bucket < month_end
              AND bucket < cutoff_ts
            ON CONFLICT DO NOTHING;

            GET DIAGNOSTICS archived_count = ROW_COUNT;

            DELETE FROM credential_model_index
            WHERE bucket >= month_start
              AND bucket < month_end
              AND bucket < cutoff_ts;

            GET DIAGNOSTICS deleted_count = ROW_COUNT;

            RETURN QUERY SELECT 'success'::text, archived_count, (deleted_count > 0);
        END;
    $$;

COMMENT ON FUNCTION public.archive_credential_model_index(archive_month date) IS
    'Archive one month of credential_model_index data (older than 7 days) into
credential_model_index_archive (columnar). Returns (status, rows_migrated,
partition_dropped) — pre-654 body with DELETE, only safe for heap source
partitions.';