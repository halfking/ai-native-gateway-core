-- V367 down deliberately preserves the safe atomic promote function.
BEGIN;
DO $do$
BEGIN
    IF to_regprocedure('public.promote_candidate_failure_logs_hot_to_partition(interval,integer)') IS NULL THEN
        RAISE EXCEPTION 'V367 down blocked: safe promote function is missing';
    END IF;
END
$do$;
COMMIT;
