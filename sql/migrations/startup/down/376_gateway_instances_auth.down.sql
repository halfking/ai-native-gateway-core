-- 376_gateway_instances_auth.down.sql
-- 撤销 gateway_instances 认证字段

BEGIN;

DROP INDEX IF EXISTS idx_gi_refresh_token;
DROP INDEX IF EXISTS idx_gi_deployment;
DROP INDEX IF EXISTS idx_gi_license;

ALTER TABLE gateway_instances
    DROP COLUMN IF EXISTS replica_count,
    DROP COLUMN IF EXISTS deployment_id,
    DROP COLUMN IF EXISTS instance_type,
    DROP COLUMN IF EXISTS hardware_hash,
    DROP COLUMN IF EXISTS license_key_hash,
    DROP COLUMN IF EXISTS current_version,
    DROP COLUMN IF EXISTS public_key,
    DROP COLUMN IF EXISTS refresh_token,
    DROP COLUMN IF EXISTS instance_token;

COMMIT;
