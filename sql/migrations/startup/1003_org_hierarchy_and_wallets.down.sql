-- Down migration for 1003_org_hierarchy_and_wallets
BEGIN;

DROP INDEX IF EXISTS idx_user_credit_wallets_tenant;
DROP TABLE IF EXISTS user_credit_wallets;

DROP INDEX IF EXISTS idx_org_credit_wallets_tenant;
DROP TABLE IF EXISTS org_credit_wallets;

DROP INDEX IF EXISTS idx_users_org;
ALTER TABLE users DROP COLUMN IF EXISTS org_id;

DROP TABLE IF EXISTS organizations;


-- ★ 台账自登记的回滚半边（与 up 件的 INSERT 对称；633.down 同款）。
DELETE FROM public.schema_migrations WHERE version = '1003';

COMMIT;
