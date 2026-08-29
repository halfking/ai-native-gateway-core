-- Rollback Migration 620: provider error aggregation watermark state.

BEGIN;

ALTER TABLE IF EXISTS public.candidate_failure_logs_hot
    ALTER COLUMN aggregation_id DROP DEFAULT;

DROP INDEX IF EXISTS public.idx_cfl_hot_aggregation_id;
DROP TABLE IF EXISTS public.provider_error_aggregator_state;
ALTER SEQUENCE IF EXISTS public.candidate_failure_logs_hot_aggregation_id_seq
    OWNED BY NONE;
DROP SEQUENCE IF EXISTS public.candidate_failure_logs_hot_aggregation_id_seq;

ALTER TABLE IF EXISTS public.candidate_failure_logs_hot
    DROP COLUMN IF EXISTS aggregation_id;

COMMIT;
