-- 083-ursm-key-migration-add-dual-checkpoint.sql
-- Add the durable dual-mode checkpoint required between coverage and observe.
-- 080 is immutable once applied, so replace only its named CHECK constraint.

BEGIN;

ALTER TABLE ursm_key_migration_runs
    DROP CONSTRAINT IF EXISTS ursm_key_migration_runs_checkpoint_chk;
ALTER TABLE ursm_key_migration_runs
    ADD CONSTRAINT ursm_key_migration_runs_checkpoint_chk CHECK (
        checkpoint IN ('preflight','copy','coverage','dual','observe','cleanup','rollback','done')
    );

COMMIT;
