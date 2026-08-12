-- Down migration for 351: remove probe_revert_at columns.
BEGIN;
DROP INDEX IF EXISTS idx_credential_model_bindings_probe_revert;
DROP INDEX IF EXISTS idx_credentials_probe_revert;
ALTER TABLE credential_model_bindings DROP COLUMN IF EXISTS probe_revert_at;
ALTER TABLE credentials DROP COLUMN IF EXISTS probe_revert_at;
COMMIT;
