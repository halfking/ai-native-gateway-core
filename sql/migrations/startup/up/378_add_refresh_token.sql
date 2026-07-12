-- 378_add_refresh_token.sql
-- Add refresh_token support to gateway_instances

ALTER TABLE gateway_instances
ADD COLUMN IF NOT EXISTS refresh_token TEXT,
ADD COLUMN IF NOT EXISTS refresh_token_issued_at TIMESTAMPTZ,
ADD COLUMN IF NOT EXISTS refresh_token_expires_at TIMESTAMPTZ,
ADD COLUMN IF NOT EXISTS license_key_hash TEXT;

-- Index for fast refresh_token lookup
CREATE INDEX IF NOT EXISTS idx_gi_refresh_token ON gateway_instances (refresh_token) WHERE refresh_token IS NOT NULL;

-- Index for license_key_hash lookup
CREATE INDEX IF NOT EXISTS idx_gi_license_key_hash ON gateway_instances (license_key_hash) WHERE license_key_hash IS NOT NULL;

COMMENT ON COLUMN gateway_instances.refresh_token IS 'Long-lived refresh token (90 days), used to obtain new instance_token';
COMMENT ON COLUMN gateway_instances.refresh_token_issued_at IS 'When the refresh_token was issued';
COMMENT ON COLUMN gateway_instances.refresh_token_expires_at IS 'When the refresh_token expires (90 days from issued_at)';
COMMENT ON COLUMN gateway_instances.license_key_hash IS 'SHA256 hash of the license key for binding instance_token';
