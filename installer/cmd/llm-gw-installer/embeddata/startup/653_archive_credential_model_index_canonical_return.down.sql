-- Rollback for 647_archive_credential_model_index_canonical_return.sql
--
-- Restore the previous (status, rows_archived, rows_deleted) return shape.
-- The function body is reverted verbatim to deploy/sql/schemas/baseline/01-schema.sql
-- pre-653 state; no data is touched (archive tables and main partitions are
-- unchanged by the column rename). The original code path that consumed
-- rows_archived/rows_deleted no longer exists in the codebase, so calling
-- this down is only safe if no caller reads rows_migrated/partition_dropped
-- from this function.

\set ON_ERROR_STOP on

DROP FUNCTION IF EXISTS public.archive_credential_model_index(date);

CREATE OR REPLACE FUNCTION public.archive_credential_model_index(archive_month date)
    RETURNS TABLE(status text, rows_archived bigint, rows_deleted bigint)
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

            RETURN QUERY SELECT 'success'::text, archived_count, deleted_count;
        END;
    $$;

COMMENT ON FUNCTION public.archive_credential_model_index(archive_month date) IS
    'Archive one month of credential_model_index data (older than 7 days) into
credential_model_index_archive (columnar). Uses TRUNCATE-then-INSERT to be
idempotent (columnar storage does not support ON CONFLICT). Deletes archived
rows from the main partitioned table to keep it lean. Run monthly on day 1.
Fixed 2026-06-30 in migration 318 (was using ON CONFLICT DO NOTHING).
Returns (status, rows_archived, rows_deleted) — pre-653 shape.';