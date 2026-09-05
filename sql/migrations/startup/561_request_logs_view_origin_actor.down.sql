-- Migration 561 (down): drop origin_actor from request_logs_with_current_month VIEW
--
-- Used by `bash scripts/sql-rollback.sh 561` (rule 38 §3).
-- Safe: origin_actor remains on hot/parent tables (341); only VIEW projection removed.

BEGIN;

DO $$
DECLARE
  drop_cols text[] := ARRAY['origin_actor'];
  base_cols text;
  view_exists boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_views
     WHERE viewname = 'request_logs_with_current_month'
       AND schemaname = 'public'
  ) INTO view_exists;

  IF NOT view_exists THEN
    RAISE NOTICE '561 down: VIEW not present — skip rebuild';
    RETURN;
  END IF;

  SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
    INTO base_cols
    FROM pg_attribute a
    JOIN pg_class c ON a.attrelid = c.oid
    JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'public'
     AND c.relname = 'request_logs_with_current_month'
     AND c.relkind = 'v'
     AND a.attnum > 0 AND NOT a.attisdropped
     AND a.attname <> ALL(drop_cols);

  IF base_cols IS NULL OR base_cols = '' THEN
    RAISE EXCEPTION '561 down: existing VIEW has no columns after drop';
  END IF;

  EXECUTE 'DROP VIEW IF EXISTS request_logs_with_current_month';
  EXECUTE format($sql$
    CREATE VIEW request_logs_with_current_month AS
    SELECT %s FROM request_logs_hot
    UNION ALL
    SELECT %s FROM request_logs
  $sql$, base_cols, base_cols);
END $$;

COMMIT;
