-- Migration 491 (down): drop V3.1 queue timestamp columns
--
-- Used by `bash scripts/sql-rollback.sh 491` (rule 38 §3).
-- Safe: columns are nullable TIMESTAMPTZ with no DEFAULT, no FK.
-- View is rebuilt without the dropped columns (448/459 append reverse).

BEGIN;

DROP INDEX IF EXISTS idx_request_logs_hot_t0_arrived;

ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS t0_arrived_at,
    DROP COLUMN IF EXISTS t1_total_enqueued_at,
    DROP COLUMN IF EXISTS t2_total_dequeued_at,
    DROP COLUMN IF EXISTS t3_model_enqueued_at,
    DROP COLUMN IF EXISTS t4_model_dequeued_at,
    DROP COLUMN IF EXISTS t5_cred_enqueued_at,
    DROP COLUMN IF EXISTS t6_cred_dequeued_at,
    DROP COLUMN IF EXISTS t7_forward_start_at,
    DROP COLUMN IF EXISTS t8_response_start_at,
    DROP COLUMN IF EXISTS t9_response_end_at;

ALTER TABLE request_logs
    DROP COLUMN IF EXISTS t0_arrived_at,
    DROP COLUMN IF EXISTS t1_total_enqueued_at,
    DROP COLUMN IF EXISTS t2_total_dequeued_at,
    DROP COLUMN IF EXISTS t3_model_enqueued_at,
    DROP COLUMN IF EXISTS t4_model_dequeued_at,
    DROP COLUMN IF EXISTS t5_cred_enqueued_at,
    DROP COLUMN IF EXISTS t6_cred_dequeued_at,
    DROP COLUMN IF EXISTS t7_forward_start_at,
    DROP COLUMN IF EXISTS t8_response_start_at,
    DROP COLUMN IF EXISTS t9_response_end_at;

-- Rebuild view without the dropped columns (keep remaining column set).
DO $$
DECLARE
  drop_cols text[] := ARRAY[
    't0_arrived_at',
    't1_total_enqueued_at',
    't2_total_dequeued_at',
    't3_model_enqueued_at',
    't4_model_dequeued_at',
    't5_cred_enqueued_at',
    't6_cred_dequeued_at',
    't7_forward_start_at',
    't8_response_start_at',
    't9_response_end_at'
  ];
  base_cols text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_views WHERE viewname = 'request_logs_with_current_month') THEN
    RETURN;
  END IF;

  SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
    INTO base_cols
    FROM pg_attribute a
    JOIN pg_class c ON a.attrelid = c.oid
   WHERE c.relname = 'request_logs_with_current_month'
     AND a.attnum > 0 AND NOT a.attisdropped
     AND a.attname <> ALL(drop_cols);

  IF base_cols IS NULL OR base_cols = '' THEN
    RAISE EXCEPTION '491 down: cannot rebuild VIEW column list';
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
