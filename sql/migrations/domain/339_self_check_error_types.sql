-- Migration 339: allow all self-check error categories emitted by the worker.
BEGIN;

ALTER TABLE self_check_runs
    DROP CONSTRAINT IF EXISTS self_check_runs_error_type_check;

COMMIT;
