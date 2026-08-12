-- Migration 486 (down): drop probe_revert_at column from credential_model_bindings
--
-- Used by `bash scripts/sql-rollback.sh 486` (rule 38 §3).
-- Safe: column is nullable TIMESTAMPTZ with no DEFAULT, no FK references.
-- Referenced by bg/probe_rollback.go + bg/node_probe.go (tentative
-- restore probe flow).

BEGIN;

ALTER TABLE credential_model_bindings
    DROP COLUMN IF EXISTS probe_revert_at;

COMMIT;
