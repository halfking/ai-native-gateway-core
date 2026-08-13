-- Migration 491: Add V3.1 9-stage dispatch queue timestamps to request_logs*
--
-- 日期: 2026-08-13
--
-- Purpose
-- ───────
-- Persist T0–T9 lifecycle timestamps collected by domains/dispatch so that:
--   1. GET /api/admin/dispatch/waterfall can be backed by durable history
--      (not only the in-memory ring of the last 200 completions)
--   2. request detail / analytics can show queue wait vs upstream TTFB
--      vs streaming duration per request
--
-- Columns (all nullable TIMESTAMPTZ):
--   t0_arrived_at          Stage 1  request arrival
--   t1_total_enqueued_at   Stage 2  total/admission queue enqueue
--   t2_total_dequeued_at   Stage 3  total queue dequeue
--   t3_model_enqueued_at   Stage 4  model queue enqueue
--   t4_model_dequeued_at   Stage 5  model queue dequeue
--   t5_cred_enqueued_at    Stage 6  credential queue enqueue
--   t6_cred_dequeued_at    Stage 7  credential queue dequeue (governor acquired)
--   t7_forward_start_at    Stage 8  forward to upstream
--   t8_response_start_at   Stage 9  first response byte
--   t9_response_end_at     Stage 10 response stream completed
--
-- Tables:
--   request_logs_hot  — write path (telemetry INSERT)
--   request_logs      — parent partitioned table (promote SELECT * requires parity)
--
-- View freeze (rule 49 §9.2 / rule 38 §5.1 2b):
--   Rebuild request_logs_with_current_month using the 448/459 append pattern
--   so admin SELECTs that project the new columns do not 42703.
--
-- Idempotent: YES (IF NOT EXISTS + view early-return)
-- Down: 491_request_logs_queue_timestamps.down.sql
-- Breaking: NO

BEGIN;

-- 1. Columns on hot + parent (same order → promote SELECT * stays aligned)
ALTER TABLE request_logs_hot
    ADD COLUMN IF NOT EXISTS t0_arrived_at        TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t1_total_enqueued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t2_total_dequeued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t3_model_enqueued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t4_model_dequeued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t5_cred_enqueued_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t6_cred_dequeued_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t7_forward_start_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t8_response_start_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t9_response_end_at   TIMESTAMPTZ;

ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS t0_arrived_at        TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t1_total_enqueued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t2_total_dequeued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t3_model_enqueued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t4_model_dequeued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t5_cred_enqueued_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t6_cred_dequeued_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t7_forward_start_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t8_response_start_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS t9_response_end_at   TIMESTAMPTZ;

COMMENT ON COLUMN request_logs_hot.t0_arrived_at IS
  'V3.1 dispatch T0: request arrival (domains/dispatch QueuedRequest.T0_ArrivedAt)';
COMMENT ON COLUMN request_logs_hot.t6_cred_dequeued_at IS
  'V3.1 dispatch T6: credential queue dequeue / governor acquired';
COMMENT ON COLUMN request_logs_hot.t9_response_end_at IS
  'V3.1 dispatch T9: response stream completed';

-- Partial index for recent waterfall queries (hot only; low write impact)
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_t0_arrived
    ON request_logs_hot (t0_arrived_at DESC)
    WHERE t0_arrived_at IS NOT NULL;

-- 2. View freeze: append new columns to request_logs_with_current_month
DO $$
DECLARE
  new_cols text[] := ARRAY[
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
  final_cols text;
  missing_count int;
  view_exists boolean;
  vc record;
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
      RAISE NOTICE '491: VIEW already exposes queue timestamp columns — skip recreate';
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
      RAISE EXCEPTION '491: existing VIEW has no columns';
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
      RAISE EXCEPTION '491: cannot build fallback column list';
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
    'Hot + monthly partitions UNION. Recreated by migration 491 (2026-08-13) '
    'to expose t0_arrived_at..t9_response_end_at (V3.1 dispatch queue timestamps). '
    'Preserves prior VIEW column set to avoid hot/parent type drift (see 448/459).';

  FOR vc IN SELECT unnest(new_cols) AS col
  LOOP
    IF NOT EXISTS (
      SELECT 1
        FROM information_schema.columns
       WHERE table_name = 'request_logs_with_current_month'
         AND column_name = vc.col
    ) THEN
      RAISE EXCEPTION '491: VIEW missing column % after recreate', vc.col;
    END IF;
  END LOOP;

  PERFORM t0_arrived_at, t6_cred_dequeued_at, t9_response_end_at
    FROM request_logs_with_current_month
   LIMIT 1;

  RAISE NOTICE 'Migration 491 completed: queue timestamps on hot/parent + VIEW';
END $$;

COMMIT;

-- POST_CONDITION:
--   SELECT column_name FROM information_schema.columns
--    WHERE table_name IN ('request_logs_hot','request_logs','request_logs_with_current_month')
--      AND column_name LIKE 't%_at' ORDER BY 1;
