-- Migration 487 (down): drop system_fingerprint column from request_logs
--
-- Used by `bash scripts/sql-rollback.sh 487` (rule 38 §3).
-- Safe: column is nullable TEXT with no DEFAULT, no FK references.

BEGIN;

ALTER TABLE request_logs
    DROP COLUMN IF EXISTS system_fingerprint;

COMMIT;
