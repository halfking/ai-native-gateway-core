-- Rollback Migration 614.
-- Keep the down migration conservative: only remove columns introduced by 614
-- when they are present. Run only after all writers have been rolled back.

BEGIN;

-- The parent already owned both columns before this repair (V358/V300).
-- Only the hot-table columns are introduced by V614.
ALTER TABLE IF EXISTS public.candidate_failure_logs_hot
    DROP COLUMN IF EXISTS session_id,
    DROP COLUMN IF EXISTS per_attempt_latency_ms;

COMMIT;
