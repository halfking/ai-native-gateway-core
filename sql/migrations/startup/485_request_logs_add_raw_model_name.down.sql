-- Migration 485 (down): drop raw_model_name column from request_logs
--
-- Used by `bash scripts/sql-rollback.sh 485` (rule 38 §3).
-- Safe: column is nullable TEXT with no DEFAULT, no FK references.
-- Only bg/integrity_fingerprint_drift.go references it.

BEGIN;

ALTER TABLE request_logs
    DROP COLUMN IF EXISTS raw_model_name;

COMMIT;
