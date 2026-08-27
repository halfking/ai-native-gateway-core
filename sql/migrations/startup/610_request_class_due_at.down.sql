-- Down migration 610: drop request class columns.
-- WARNING: view rebuilt WITHOUT the two columns first (view freeze), then
-- columns dropped from hot + parent. Data loss: request_class/due_at values.

BEGIN;

DO $$
DECLARE
  drop_cols text[] := ARRAY['request_class', 'due_at'];
  base_cols text;
  view_exists boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'request_logs_with_current_month'
  ) INTO view_exists;

  IF view_exists THEN
    SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
      INTO base_cols
      FROM pg_attribute a
      JOIN pg_class c ON a.attrelid = c.oid
     WHERE c.relname = 'request_logs_with_current_month'
       AND a.attnum > 0 AND NOT a.attisdropped
       AND a.attname <> ALL(drop_cols);

    IF base_cols IS NOT NULL AND base_cols <> '' THEN
      EXECUTE format(
        'CREATE OR REPLACE VIEW request_logs_with_current_month AS SELECT %s FROM request_logs',
        base_cols
      );
    END IF;
  END IF;
END $$;

DROP INDEX IF EXISTS idx_request_logs_hot_request_class;
ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS request_class,
    DROP COLUMN IF EXISTS due_at;
ALTER TABLE request_logs
    DROP COLUMN IF EXISTS request_class,
    DROP COLUMN IF EXISTS due_at;

COMMIT;
