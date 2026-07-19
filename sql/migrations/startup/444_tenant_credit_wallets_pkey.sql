-- Migration 2026-07-12: Add PRIMARY KEY to tenant_credit_wallets
--
-- Background:
--   tenant_credit_wallets was originally created without PRIMARY KEY in baseline
--   schemas (deploy/sql/objects/tables/tenant_credit_wallets.sql and
--   deploy/sql/schemas/baseline/01-schema.sql). Migration 007_maas_billing.sql
--   had `tenant_id VARCHAR(64) PRIMARY KEY` but on prod it was applied without
--   the PK. As a result, every ensureWallet() INSERT created a duplicate row
--   instead of triggering ON CONFLICT DO NOTHING. After ~weeks of operations
--   we have 32 rows for 8 distinct tenants.
--
-- Symptoms (fixed by this migration):
--   - admin/maas grant endpoint returned
--       ERROR: there is no unique or exclusion constraint matching the
--       ON CONFLICT specification (SQLSTATE 42P10)
--   - balance reads could return any one of the duplicates (non-deterministic)
--   - UPDATE ... RETURNING balance_credits only updates ONE row but reads
--     see different rows
--
-- Steps:
--   1. Deduplicate: keep one row per tenant (the latest one — max updated_at
--      or max(ctid) as tie-breaker). Sum balances for the duplicates so we
--      don't lose credit.
--   2. Add PRIMARY KEY (tenant_id).
--   3. Verify final state.

BEGIN;

-- The source table may already be repaired on a partially migrated target.
-- Keep the data rewrite and constraint creation safe to retry as one unit.

-- Step 1: dedupe by tenant_id
-- 1a. Create a dedup'd mirror table with merged balances.
DROP TABLE IF EXISTS tenant_credit_wallets_dedup;
CREATE TABLE IF NOT EXISTS tenant_credit_wallets_dedup AS
SELECT
    tenant_id,
    -- Take the LATEST balance_credits (per updated_at, then ctid as tie-breaker)
    (SELECT balance_credits FROM tenant_credit_wallets w
       WHERE w.tenant_id = src.tenant_id
       ORDER BY updated_at DESC, ctid DESC LIMIT 1) AS balance_credits,
    -- Sum granted_balance + purchased_balance across all duplicates to avoid
    -- losing any credits already injected.
    SUM(granted_balance)::BIGINT   AS granted_balance,
    SUM(purchased_balance)::BIGINT AS purchased_balance,
    -- Use the latest updated_at for the dedup'd row.
    MAX(updated_at) AS updated_at,
    -- Take the latest locked_credits too.
    (SELECT locked_credits FROM tenant_credit_wallets w
       WHERE w.tenant_id = src.tenant_id
       ORDER BY updated_at DESC, ctid DESC LIMIT 1) AS locked_credits
FROM tenant_credit_wallets src
GROUP BY tenant_id;

-- 1b. Recompute balance_credits = granted_balance + purchased_balance
--     (matches GrantCredits()'s UPDATE formula in maas/orders.go).
UPDATE tenant_credit_wallets_dedup
   SET balance_credits = granted_balance + purchased_balance;

-- 1c. Replace the original table content
DELETE FROM tenant_credit_wallets;

INSERT INTO tenant_credit_wallets
    (tenant_id, balance_credits, locked_credits, updated_at,
     granted_balance, purchased_balance)
SELECT
    tenant_id, balance_credits, locked_credits, updated_at,
    granted_balance, purchased_balance
FROM tenant_credit_wallets_dedup;

DROP TABLE tenant_credit_wallets_dedup;

-- Step 2: add PRIMARY KEY
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'tenant_credit_wallets'::regclass
          AND contype = 'p'
    ) THEN
        ALTER TABLE tenant_credit_wallets
            ADD CONSTRAINT tenant_credit_wallets_pkey PRIMARY KEY (tenant_id);
    END IF;
END $$;

COMMIT;

-- Step 3: verify
SELECT
    (SELECT COUNT(*) FROM tenant_credit_wallets)             AS total_rows,
    (SELECT COUNT(DISTINCT tenant_id) FROM tenant_credit_wallets) AS distinct_tenants,
    (SELECT relhaspkey FROM pg_class WHERE relname='tenant_credit_wallets') AS has_pkey;
