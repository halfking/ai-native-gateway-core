-- V362: align candidate_failure_logs_hot with the candidate failure writer.
-- The writer explicitly inserts session_id and per_attempt_latency_ms. Older hot
-- tables did not have either column, causing the INSERT to fail after V359.

BEGIN;

ALTER TABLE IF EXISTS public.candidate_failure_logs
    ADD COLUMN IF NOT EXISTS session_id text,
    ADD COLUMN IF NOT EXISTS per_attempt_latency_ms integer;

ALTER TABLE IF EXISTS public.candidate_failure_logs_hot
    ADD COLUMN IF NOT EXISTS session_id text,
    ADD COLUMN IF NOT EXISTS per_attempt_latency_ms integer;

COMMIT;
