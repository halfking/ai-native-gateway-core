-- Migration 478p2 down: remove affinity worker indexes.
--
-- These indexes only optimize affinity worker reads. Removing them preserves
-- request data and learned affinity rankings, but can make worker scans slower.

\set ON_ERROR_STOP on
BEGIN;

DROP INDEX IF EXISTS public.idx_request_logs_hot_gw_session_id;
DROP INDEX IF EXISTS public.idx_request_logs_hot_auto_task_ts;

COMMIT;
