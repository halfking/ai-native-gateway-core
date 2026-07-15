ALTER TABLE instance_heartbeats DROP COLUMN IF EXISTS metrics;

ALTER TABLE gateway_instances
    DROP COLUMN IF EXISTS replica_count,
    DROP COLUMN IF EXISTS deployment_id,
    DROP COLUMN IF EXISTS instance_type,
    DROP COLUMN IF EXISTS hardware_hash,
    DROP COLUMN IF EXISTS license_key_hash,
    DROP COLUMN IF EXISTS current_version,
    DROP COLUMN IF EXISTS public_key,
    DROP COLUMN IF EXISTS refresh_token_expires_at,
    DROP COLUMN IF EXISTS refresh_token_issued_at,
    DROP COLUMN IF EXISTS refresh_token,
    DROP COLUMN IF EXISTS instance_token;
