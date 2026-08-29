-- Migration 614: align candidate_failure_logs_hot with the candidate failure writer.
-- The writer explicitly inserts session_id and per_attempt_latency_ms. Older hot
-- tables (including the original 392 shape) did not have either column, so the
-- INSERT failed after V359 moved the write path to the heap hot table.
--
-- Forward-compatible and idempotent: both columns are nullable because older
-- rows and legacy writers do not provide them.

BEGIN;

ALTER TABLE IF EXISTS public.candidate_failure_logs
    ADD COLUMN IF NOT EXISTS session_id text,
    ADD COLUMN IF NOT EXISTS per_attempt_latency_ms integer;

ALTER TABLE IF EXISTS public.candidate_failure_logs_hot
    ADD COLUMN IF NOT EXISTS session_id text,
    ADD COLUMN IF NOT EXISTS per_attempt_latency_ms integer;

COMMIT;
