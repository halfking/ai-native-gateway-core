-- Rollback V362. Run only after all writers have been rolled back.

BEGIN;

-- The parent already owned both columns before this repair (V358/V300).
-- Only remove the columns added to the legacy hot table.
ALTER TABLE IF EXISTS public.candidate_failure_logs_hot
    DROP COLUMN IF EXISTS session_id,
    DROP COLUMN IF EXISTS per_attempt_latency_ms;

COMMIT;
