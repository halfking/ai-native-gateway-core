-- 390_gateway_instances_center_agent.sql
-- Align 252 data-plane gateway_instances / instance_heartbeats with center agent.

ALTER TABLE gateway_instances
    ADD COLUMN IF NOT EXISTS instance_token    TEXT,
    ADD COLUMN IF NOT EXISTS refresh_token     TEXT,
    ADD COLUMN IF NOT EXISTS refresh_token_issued_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS refresh_token_expires_at TIMESTAMPTZ,
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

ALTER TABLE instance_heartbeats
    ADD COLUMN IF NOT EXISTS metrics JSONB;
