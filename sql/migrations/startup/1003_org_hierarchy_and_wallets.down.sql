-- Down migration for 1003_org_hierarchy_and_wallets
BEGIN;

DROP INDEX IF EXISTS idx_user_credit_wallets_tenant;
DROP TABLE IF EXISTS user_credit_wallets;

DROP INDEX IF EXISTS idx_org_credit_wallets_tenant;
DROP TABLE IF EXISTS org_credit_wallets;

DROP INDEX IF EXISTS idx_users_org;
ALTER TABLE users DROP COLUMN IF EXISTS org_id;

DROP TABLE IF EXISTS organizations;

COMMIT;
