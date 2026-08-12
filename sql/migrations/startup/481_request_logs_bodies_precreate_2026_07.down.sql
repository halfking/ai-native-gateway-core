-- Migration 481 (down): drop request_logs_bodies_2026_07 partition
--
-- Used by `bash scripts/sql-rollback.sh 481` (rule 38 §3).
-- Safe: only drops if exists. Does NOT cascade to the data because the
-- partition is empty by the time of rollback (cron promote has long since
-- drained 2026-07 hot rows into the new partition).

BEGIN;

DROP TABLE IF EXISTS public.request_logs_bodies_2026_07;

COMMIT;
