-- Rollback Migration 627: drop unified view + aggregation_id column.
-- The down is intentionally lossy: dropping the column re-introduces the
-- missing-bucket bug described in the up file. Operators running the down
-- must follow the audit handoff's remediation (re-seed the watermark below
-- the maximum historical aggregation_id BEFORE the next aggregator tick,
-- otherwise promoted historical rows are lost again).

BEGIN;

DROP VIEW IF EXISTS public.candidate_failure_logs_unified;

-- Best-effort: drop the index, then the column. We do NOT drop the backfilled
-- sequence numbers; they are owned by candidate_failure_logs_hot so dropping
-- the column here does not affect the sequence.
DROP INDEX IF EXISTS public.idx_candidate_failure_logs_aggregation_id;

ALTER TABLE IF EXISTS public.candidate_failure_logs
    DROP COLUMN IF EXISTS aggregation_id;

COMMIT;
