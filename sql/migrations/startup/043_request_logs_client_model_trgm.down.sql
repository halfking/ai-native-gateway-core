-- Rollback for migration 043_request_logs_client_model_trgm.sql.
-- Idempotent. Drops the parent (partitioned) index, every per-partition
-- child index, and restores ensure_request_logs_partition() to its
-- pre-043 form (only the search_text trgm index per new partition).

-- 1. Drop each per-partition child index first. The parent partitioned
--    index cannot be dropped cleanly while its children still reference
--    it via ATTACH, so we drop children explicitly. (DROP INDEX on the
--    parent also cascades to children on PG14+, but being explicit is
--    safe across versions.)
DO $$
DECLARE
    part text;
    child_idx text;
BEGIN
    FOR part IN
        SELECT inhrelid::regclass::text
        FROM pg_inherits
        WHERE inhparent = 'request_logs'::regclass
        ORDER BY 1
    LOOP
        child_idx := 'idx_' || part || '_client_model_trgm';
        EXECUTE format('DROP INDEX IF EXISTS %I', child_idx);
    END LOOP;
END$$;

-- 2. Drop the parent (partitioned) index shell.
DROP INDEX IF EXISTS public.idx_request_logs_client_model_trgm;

-- 3. Restore ensure_request_logs_partition() to its pre-043 form
--    (only search_text trgm per new partition).
CREATE OR REPLACE FUNCTION public.ensure_request_logs_partition(target_ts timestamptz DEFAULT now())
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    month_start   date := date_trunc('month', target_ts)::date;
    month_end     date := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name     text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end
        );
        EXECUTE format(
            'CREATE INDEX idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
            part_name, part_name
        );
    END IF;
END;
$$;
