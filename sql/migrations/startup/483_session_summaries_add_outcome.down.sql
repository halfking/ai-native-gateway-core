-- Migration 483 (down): drop outcome column from session_summaries
--
-- Used by `bash scripts/sql-rollback.sh 483` (rule 38 §3).
-- Safe: column is nullable TEXT with no DEFAULT, no FK references, and
-- only the session health worker writes to it (which has been failing
-- with SQLSTATE 42703 until this migration applied anyway).

BEGIN;

ALTER TABLE session_summaries
    DROP COLUMN IF EXISTS outcome;

COMMIT;
