-- Rollback for 080-ursm-key-migration-ledger.sql.
-- Drops both ledger tables and their indexes. The schema is owned by
-- migration 080; do not run this before inspecting any in-flight
-- migration run.

BEGIN;

DROP INDEX IF EXISTS ursm_key_migration_entries_class_idx;
DROP INDEX IF EXISTS ursm_key_migration_entries_state_idx;
DROP TABLE IF EXISTS ursm_key_migration_entries;
DROP TABLE IF EXISTS ursm_key_migration_runs;

COMMIT;