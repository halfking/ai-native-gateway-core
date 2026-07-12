-- 376_gateway_instances_auth.sql
-- 为 gateway_instances 补充 license-authority 所需字段

ALTER TABLE gateway_instances
    ADD COLUMN IF NOT EXISTS instance_token    TEXT,
    ADD COLUMN IF NOT EXISTS refresh_token     TEXT,
    ADD COLUMN IF NOT EXISTS public_key        TEXT,
    ADD COLUMN IF NOT EXISTS current_version   TEXT,
    ADD COLUMN IF NOT EXISTS license_key_hash  TEXT,
    ADD COLUMN IF NOT EXISTS hardware_hash     TEXT,
    ADD COLUMN IF NOT EXISTS instance_type     TEXT DEFAULT 'standalone',
    ADD COLUMN IF NOT EXISTS deployment_id     TEXT,
    ADD COLUMN IF NOT EXISTS replica_count     INT DEFAULT 1;

CREATE INDEX IF NOT EXISTS idx_gi_license ON gateway_instances (license_key_hash);
CREATE INDEX IF NOT EXISTS idx_gi_deployment ON gateway_instances (deployment_id);
CREATE INDEX IF NOT EXISTS idx_gi_refresh_token ON gateway_instances (refresh_token);

COMMENT ON COLUMN gateway_instances.instance_token IS 'JWT token for instance authentication';
COMMENT ON COLUMN gateway_instances.refresh_token IS 'Refresh token for token renewal';
COMMENT ON COLUMN gateway_instances.public_key IS 'Instance public key for signature verification';
COMMENT ON COLUMN gateway_instances.current_version IS 'Current running version';
COMMENT ON COLUMN gateway_instances.license_key_hash IS 'Hash of bound license key';
COMMENT ON COLUMN gateway_instances.hardware_hash IS 'Hardware fingerprint';
COMMENT ON COLUMN gateway_instances.instance_type IS 'standalone / k8s-deployment / docker';
COMMENT ON COLUMN gateway_instances.deployment_id IS 'K8s: namespace/deployment-name, Docker: container-id';
COMMENT ON COLUMN gateway_instances.replica_count IS 'Number of replicas in deployment';
