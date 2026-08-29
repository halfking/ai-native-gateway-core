-- Migration 624 down deliberately keeps the safe promote implementation.
-- Restoring V359's delete-before-insert function would reintroduce silent loss.
BEGIN;
DO $do$
BEGIN
    IF to_regprocedure('public.promote_candidate_failure_logs_hot_to_partition(interval,integer)') IS NULL THEN
        RAISE EXCEPTION 'migration 624 down blocked: safe promote function is missing';
    END IF;
END
$do$;
COMMIT;
