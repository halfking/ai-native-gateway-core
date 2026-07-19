-- Rollback for 444_tenant_credit_wallets_pkey.sql.
-- This removes the constraint only. Duplicate rows and the original row-level
-- history cannot be reconstructed after the deduplication without a backup.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'tenant_credit_wallets'::regclass
          AND conname = 'tenant_credit_wallets_pkey'
    ) THEN
        ALTER TABLE tenant_credit_wallets
            DROP CONSTRAINT tenant_credit_wallets_pkey;
    END IF;
END $$;

COMMIT;
