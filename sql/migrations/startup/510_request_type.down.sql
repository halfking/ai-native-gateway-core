-- Migration 510 (down): drop request_type column
--
-- Used by `bash scripts/sql-rollback.sh 510` (rule 38 §3).
-- Safe: column has DEFAULT 'main', no FK.
-- View is rebuilt without the dropped column (448/459/491 append reverse).

BEGIN;

DROP INDEX IF EXISTS idx_request_logs_hot_request_type;

ALTER TABLE request_logs_hot
    DROP CONSTRAINT IF EXISTS chk_request_logs_hot_request_type;
ALTER TABLE request_logs
    DROP CONSTRAINT IF EXISTS chk_request_logs_request_type;

-- 先重建视图（去掉 request_type 列），解除视图对该列的依赖，再删列。
DO $$
DECLARE
  drop_cols text[] := ARRAY['request_type'];
  base_cols text;
  view_exists boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'request_logs_with_current_month'
  ) INTO view_exists;

  IF NOT view_exists THEN
    RAISE NOTICE '510 down: VIEW not present — skip rebuild';
  ELSE
    SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
      INTO base_cols
      FROM pg_attribute a
      JOIN pg_class c ON a.attrelid = c.oid
     WHERE c.relname = 'request_logs_with_current_month'
       AND a.attnum > 0 AND NOT a.attisdropped
       AND a.attname <> ALL(drop_cols);

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '510 down: existing VIEW has no columns after drop';
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
    DROP COLUMN IF EXISTS request_type;
ALTER TABLE request_logs
    DROP COLUMN IF EXISTS request_type;

COMMIT;
