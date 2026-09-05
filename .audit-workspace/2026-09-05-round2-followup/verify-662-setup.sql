-- 662 pre-condition harness (throwaway container): production-shaped
-- provider_error_details subset + the 639-era message-scoped fingerprint +
-- three message-fragmented rows in one logical bucket.
CREATE TABLE IF NOT EXISTS provider_error_details (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider_id bigint NOT NULL,
    model_name character varying(100),
    endpoint character varying(50),
    error_type character varying(50) NOT NULL,
    error_code character varying(50),
    error_message text,
    request_id character varying(100),
    user_id character varying(100),
    tenant_id character varying(100),
    context jsonb,
    occurrences integer DEFAULT 1,
    first_seen_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    acknowledged boolean DEFAULT false,
    resolved boolean DEFAULT false,
    resolution_note text,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
ALTER TABLE provider_error_details ADD COLUMN IF NOT EXISTS aggregation_bucket TIMESTAMPTZ;
ALTER TABLE provider_error_details ADD COLUMN IF NOT EXISTS credential_id TEXT;
TRUNCATE provider_error_details;

DROP INDEX IF EXISTS idx_provider_error_details_tenant_cred_fingerprint;
CREATE UNIQUE INDEX idx_provider_error_details_tenant_cred_fingerprint
ON provider_error_details (
    COALESCE(tenant_id, ''), provider_id, COALESCE(credential_id, ''),
    COALESCE(model_name, ''), COALESCE(endpoint, ''), error_type,
    COALESCE(error_code, ''), COALESCE(LEFT(error_message, 200), ''),
    COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
);

INSERT INTO provider_error_details (provider_id, tenant_id, credential_id, model_name, endpoint, error_type, error_code, error_message, occurrences, first_seen_at, last_seen_at, aggregation_bucket)
VALUES
 (9, 'tenant-a', '42', 'glm-5.2', 'api.example.com', 'rate_limit', '1210', 'rate limited (retry after 3s)', 2, NOW()-interval '30 min', NOW()-interval '20 min', date_trunc('hour', NOW())),
 (9, 'tenant-a', '42', 'glm-5.2', 'api.example.com', 'rate_limit', '1210', 'rate limited (retry after 5s)', 3, NOW()-interval '25 min', NOW()-interval '15 min', date_trunc('hour', NOW())),
 (9, 'tenant-a', '42', 'glm-5.2', 'api.example.com', 'rate_limit', '1210', 'rate limited (retry after 8s)', 1, NOW()-interval '10 min', NOW()-interval '5 min', date_trunc('hour', NOW())),
 (9, 'tenant-b', NULL, 'glm-5.2', 'api.example.com', 'timeout', '', 'timeout after 30s', 4, NOW()-interval '40 min', NOW()-interval '10 min', date_trunc('hour', NOW()));
