-- Migration 532 (down): drop session-level final-success marker
--
-- Used by `bash scripts/sql-rollback.sh 532` (rule 38 §3).
-- Safe: column has DEFAULT FALSE, no FK. Historical is_final_success=TRUE rows
-- lose their marker — rerun 532 and the compensation report to re-derive.

BEGIN;

DROP INDEX IF EXISTS uq_request_logs_hot_final_success_session;

-- Per-partition final-success indexes (names are uq_<partition>_final_success_session).
DO $$
DECLARE
  part record;
BEGIN
  FOR part IN
    SELECT c.relname AS partition_name
      FROM pg_inherits i
      JOIN pg_class parent ON parent.oid = i.inhparent
      JOIN pg_class c ON c.oid = i.inhrelid
     WHERE parent.relname = 'request_logs'
       AND parent.relnamespace = 'public'::regnamespace
       AND c.relkind = 'r'
  LOOP
    EXECUTE format(
      'DROP INDEX IF EXISTS %I',
      'uq_' || part.partition_name || '_final_success_session'
    );
  END LOOP;
END $$;

-- restore ensure_request_logs_partition without the 532 index step
-- (body mirrors the pre-532 definition from sql/schema/01-schema.sql).
CREATE OR REPLACE FUNCTION public.ensure_request_logs_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
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

-- Rebuild the view without is_final_success (448/459/491/510 append reverse),
-- then drop the column from hot + parent (cascades to partitions).
DO $$
DECLARE
  drop_cols text[] := ARRAY['is_final_success'];
  base_cols text;
  view_exists boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'request_logs_with_current_month'
  ) INTO view_exists;

  IF NOT view_exists THEN
    RAISE NOTICE '532 down: VIEW not present — skip rebuild';
  ELSE
    SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
      INTO base_cols
      FROM pg_attribute a
      JOIN pg_class c ON a.attrelid = c.oid
     WHERE c.relname = 'request_logs_with_current_month'
       AND a.attnum > 0 AND NOT a.attisdropped
       AND a.attname <> ALL(drop_cols);

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '532 down: existing VIEW has no columns after drop';
    END IF;

    EXECUTE 'DROP VIEW IF EXISTS request_logs_with_current_month';
    EXECUTE format($sql$
      CREATE VIEW request_logs_with_current_month AS
      SELECT %s FROM request_logs_hot
      UNION ALL
      SELECT %s FROM request_logs
    $sql$, base_cols, base_cols);
  END IF;
END $$;

ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS is_final_success;
ALTER TABLE request_logs
    DROP COLUMN IF EXISTS is_final_success;

COMMIT;
