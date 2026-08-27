-- Down migration 608: remove request_class/due_at and restore the exact
-- pre-608 current-month view. The up migration saves the prior definition as
-- request_logs_with_current_month_without_request_class_due_at, so rollback
-- never uses CREATE OR REPLACE to remove view output columns.

BEGIN;

DROP VIEW IF EXISTS public.request_logs_with_current_month;

DO $$
DECLARE
  base_cols text;
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_views
    WHERE schemaname = 'public'
      AND viewname = 'request_logs_with_current_month_without_request_class_due_at'
  ) THEN
    ALTER VIEW public.request_logs_with_current_month_without_request_class_due_at
      RENAME TO request_logs_with_current_month;
  ELSE
    SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
      INTO base_cols
      FROM pg_attribute a
      JOIN pg_class c ON c.oid = a.attrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public'
       AND c.relname = 'request_logs_hot'
       AND a.attnum > 0
       AND NOT a.attisdropped
       AND a.attname NOT IN ('request_class', 'due_at');
    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '608 down: cannot build fallback base column list';
    END IF;
    EXECUTE format(
      'CREATE VIEW public.request_logs_with_current_month AS SELECT %1$s FROM public.request_logs_hot UNION ALL SELECT %1$s FROM public.request_logs',
      base_cols
    );
  END IF;
END $$;

DROP INDEX IF EXISTS public.idx_request_logs_hot_request_class;
ALTER TABLE public.request_logs_hot
    DROP CONSTRAINT IF EXISTS request_logs_hot_request_class_due_at_check,
    DROP COLUMN IF EXISTS request_class,
    DROP COLUMN IF EXISTS due_at;
ALTER TABLE public.request_logs
    DROP CONSTRAINT IF EXISTS request_logs_request_class_due_at_check,
    DROP COLUMN IF EXISTS request_class,
    DROP COLUMN IF EXISTS due_at;

COMMIT;
