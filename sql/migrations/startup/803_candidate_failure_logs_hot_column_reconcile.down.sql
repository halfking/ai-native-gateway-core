-- Rollback Migration 803.
-- Keep the down migration conservative: only remove the hot-table columns
-- introduced by this reconcile. On deploy-chain (V359) lineage databases the
-- columns predate 803 and may carry data — run only after all writers have
-- been rolled back and the columns verified empty. The parent-table columns
-- are NOT touched (300/baseline owned them long before 803).

BEGIN;

ALTER TABLE IF EXISTS public.candidate_failure_logs_hot
    DROP COLUMN IF EXISTS extracted_upstream_status_code,
    DROP COLUMN IF EXISTS diagnosed_error_kind;

COMMIT;
