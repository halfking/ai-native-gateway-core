-- 081-ursm-key-migration-ledger-add-rollback-deadline.down.sql
-- Intentionally non-destructive: rollback-gate timestamps are audit data and
-- must survive binary/migration rollback. Older binaries tolerate this column.

BEGIN;

DO $$
BEGIN
    RAISE NOTICE '081 down is non-destructive; preserving rollback_deadline audit data';
END
$$;

COMMIT;
