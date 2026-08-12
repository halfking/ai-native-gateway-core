-- Migration 484 (down): drop status_code column from request_logs_hot
--
-- Used by `bash scripts/sql-rollback.sh 484` (rule 38 §3).
-- Safe: column is nullable INTEGER with no DEFAULT, no FK references.
-- Only bg/credential_selfcheck.go references it; dropping will re-break
-- the self-check worker (which is the bug this migration fixed).

BEGIN;

ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS status_code;

COMMIT;
