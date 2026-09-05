-- 081-ursm-key-migration-ledger-add-rollback-deadline.sql
-- Forward-only repair for the deployed 080 ledger schema. PGStore.OpenRun
-- persists the rollback gate as rollback_deadline, so existing 080 databases
-- must receive this additive column without rewriting migration history.

BEGIN;

ALTER TABLE public.ursm_key_migration_runs
    ADD COLUMN IF NOT EXISTS rollback_deadline TIMESTAMPTZ;

COMMIT;
