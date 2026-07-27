-- Migration 459 down migration.
-- The view is rebuilt from the columns that remain after the client-perception
-- columns are removed. The base tables keep their columns because 458 owns
-- their lifecycle and its down migration removes them.

BEGIN;

DO $$
DECLARE
  base_cols text;
BEGIN
  SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
    INTO base_cols
    FROM pg_attribute a
    JOIN pg_class c ON a.attrelid = c.oid
   WHERE c.relname = 'request_logs_with_current_month'
     AND a.attnum > 0
     AND NOT a.attisdropped
     AND a.attname <> ALL(ARRAY['agent_name', 'agent_type', 'client_protocol', 'canonical_model', 'virtual_client_id']);

  IF base_cols IS NULL OR base_cols = '' THEN
    RAISE EXCEPTION '459 down: existing VIEW has no columns';
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
