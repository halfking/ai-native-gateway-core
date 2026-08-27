-- Migration 610: Persist request class (immediate|scheduled) + due_at
--
-- 日期: 2026-08-27
--
-- Purpose
-- ───────
-- V6-W1.6 R8 added the request class to the IR (ir.InternalRequest.Class /
-- DueAt, stamped from the X-Gw-Due-At header). Persist it so requests are
-- classifiable after the fact:
--   1. request_logs list/detail can filter & display scheduled traffic
--   2. analytics can split immediate vs scheduled behavior
--   3. the FS fallback record (internal/fsstore.RequestRecord) mirrors the
--      same two fields for the no-PG tier
--
-- Columns:
--   request_class  text NOT NULL DEFAULT 'immediate'  ('immediate'|'scheduled')
--   due_at         timestamptz NULL                   (set when scheduled)
--
-- Tables:
--   request_logs_hot  — write path (telemetry INSERT, appended as $101/$102)
--   request_logs      — parent partitioned table (promote SELECT * parity)
--
-- View freeze (rule 49 §9.2): rebuild request_logs_with_current_month using
-- the 448/459/491 append pattern so admin SELECTs projecting the new columns
-- do not 42703.
--
-- Idempotent: YES (IF NOT EXISTS + view early-return)
-- Down: 610_request_class_due_at.down.sql
-- Breaking: NO

BEGIN;

-- 1. Columns on hot + parent (same order → promote SELECT * stays aligned)
ALTER TABLE request_logs_hot
    ADD COLUMN IF NOT EXISTS request_class text NOT NULL DEFAULT 'immediate',
    ADD COLUMN IF NOT EXISTS due_at timestamptz;

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS request_class text NOT NULL DEFAULT 'immediate',
    ADD COLUMN IF NOT EXISTS due_at timestamptz;

COMMENT ON COLUMN request_logs_hot.request_class IS
  'V6-W1.6 R8 request class: immediate|scheduled (ir.InternalRequest.Class via X-Gw-Due-At)';
COMMENT ON COLUMN request_logs_hot.due_at IS
  'V6-W1.6 R8 scheduled execution time; NULL for immediate requests';

-- Scheduled-traffic filter support (hot only; partial, low write impact)
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_request_class
    ON request_logs_hot (request_class)
    WHERE request_class = 'scheduled';

-- 2. View freeze: append new columns to request_logs_with_current_month
DO $$
DECLARE
  new_cols text[] := ARRAY['request_class', 'due_at'];
  base_cols text;
  final_cols text;
  missing_count int;
  view_exists boolean;
BEGIN
  SELECT EXISTS (
    SELECT 1 FROM pg_views WHERE viewname = 'request_logs_with_current_month'
  ) INTO view_exists;

  IF view_exists THEN
    SELECT count(*) INTO missing_count
      FROM unnest(new_cols) nc
     WHERE NOT EXISTS (
       SELECT 1
         FROM information_schema.columns
        WHERE table_name = 'request_logs_with_current_month'
          AND column_name = nc
     );

    IF missing_count = 0 THEN
      RAISE NOTICE '610: VIEW already exposes request class columns — skip recreate';
      RETURN;
    END IF;

    SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum)
      INTO base_cols
      FROM pg_attribute a
      JOIN pg_class c ON a.attrelid = c.oid
     WHERE c.relname = 'request_logs_with_current_month'
       AND a.attnum > 0 AND NOT a.attisdropped
       AND a.attname <> ALL(new_cols);

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '610: existing VIEW has no columns';
    END IF;
    final_cols := base_cols || ', ' || array_to_string(new_cols, ', ');
  ELSE
    -- Cold-start: only columns present on BOTH sides with matching types.
    SELECT string_agg(quote_ident(h.attname), ', ' ORDER BY h.attnum)
      INTO base_cols
      FROM pg_attribute h
      JOIN pg_class ch ON h.attrelid = ch.oid
      JOIN pg_attribute p ON p.attname = h.attname
      JOIN pg_class cp ON p.attrelid = cp.oid
     WHERE ch.relname = 'request_logs_hot'
       AND cp.relname = 'request_logs'
       AND h.attnum > 0 AND NOT h.attisdropped
       AND p.attnum > 0 AND NOT p.attisdropped
       AND h.atttypid = p.atttypid
       AND h.attname <> ALL(new_cols);

    IF base_cols IS NULL OR base_cols = '' THEN
      RAISE EXCEPTION '610: cannot build fallback column list';
    END IF;
    final_cols := base_cols || ', ' || array_to_string(new_cols, ', ');
  END IF;

  EXECUTE 'DROP VIEW IF EXISTS request_logs_with_current_month';
  EXECUTE format($sql$
    CREATE VIEW request_logs_with_current_month AS
    SELECT %s FROM request_logs_hot
    UNION ALL
    SELECT %s FROM request_logs
  $sql$, final_cols, final_cols);

  COMMENT ON VIEW request_logs_with_current_month IS
    'Hot + monthly partitions UNION. Recreated by migration 610 (2026-08-27) '
    'to expose request_class/due_at (V6-W1.6 R8 immediate|scheduled request class). '
    'Preserves prior VIEW column set to avoid hot/parent type drift (see 448/459/491).';
END $$;

COMMIT;
