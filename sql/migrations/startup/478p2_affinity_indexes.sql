-- Migration 478p2: indexes to make the affinity settle worker cheap.
--
-- MEDIUM-2 (audit 2026-08-11): the settle worker's LATERAL scans
-- request_logs_hot by gw_session_id once per pending row (up to 500/sweep).
-- Without an index this becomes a SeqScan per row at scale.
--
-- MEDIUM-3: loadTaskBaselines scans request_logs_hot by task_type + is_auto_request
-- over a 24h window. A composite index on the filter columns makes that a
-- bounded index range scan.
--
-- Idempotent: pg_indexes name check. Re-runnable.
--
-- Rollback: DROP INDEX IF EXISTS idx_request_logs_hot_gw_session_id;
--           DROP INDEX IF EXISTS idx_request_logs_hot_auto_task_ts;

\set ON_ERROR_STOP on
BEGIN;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE schemaname = 'public' AND indexname = 'idx_request_logs_hot_gw_session_id'
  ) THEN
    CREATE INDEX idx_request_logs_hot_gw_session_id
      ON public.request_logs_hot (gw_session_id, ts DESC)
      WHERE gw_session_id IS NOT NULL;
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE schemaname = 'public' AND indexname = 'idx_request_logs_hot_auto_task_ts'
  ) THEN
    CREATE INDEX idx_request_logs_hot_auto_task_ts
      ON public.request_logs_hot (task_type, ts DESC)
      WHERE is_auto_request IS TRUE;
  END IF;
END $$;

COMMIT;