-- Migration 488 (down): drop model column from request_logs_hot
--
-- Used by `bash scripts/sql-rollback.sh 488` (rule 38 §3).
-- Safe: column is nullable TEXT with no DEFAULT, no FK references.

BEGIN;

ALTER TABLE request_logs_hot
    DROP COLUMN IF EXISTS model;

COMMIT;
