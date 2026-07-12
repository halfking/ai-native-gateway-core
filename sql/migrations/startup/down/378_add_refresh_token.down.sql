-- 378_add_refresh_token.down.sql
-- Rollback refresh_token support from gateway_instances

ALTER TABLE gateway_instances
DROP COLUMN IF EXISTS license_key_hash,
DROP COLUMN IF EXISTS refresh_token_expires_at,
DROP COLUMN IF EXISTS refresh_token_issued_at,
DROP COLUMN IF EXISTS refresh_token;

DROP INDEX IF EXISTS idx_gi_license_key_hash;
DROP INDEX IF EXISTS idx_gi_refresh_token;
