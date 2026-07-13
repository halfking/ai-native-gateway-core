-- 391_state_table_storage_hardening.down.sql
-- Reverses the state table storage hardening migration.
-- Drops the helper functions added in the up migration. Does NOT
-- restore the original 7d retention policy for credential_model_call_history
-- (operationally we want to keep the 30d improvement even on rollback).
-- To restore 7d, run manually:
--   SELECT remove_retention_policy('credential_model_call_history');
--   SELECT add_retention_policy('credential_model_call_history', INTERVAL '7 days');

BEGIN;

DROP FUNCTION IF EXISTS drop_old_state_partitions(int);
DROP FUNCTION IF EXISTS cleanup_old_credential_probe_model_log(int);

-- Index cleanup: only drop if it was added by this migration
DROP INDEX IF EXISTS idx_credential_probe_model_log_created_at;

COMMIT;
