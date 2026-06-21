-- Request WAL (Write-Ahead Log) for request lifecycle tracking
-- 2026-06-22: Phase 1 implementation

-- Create partitioned request_logs table
CREATE TABLE IF NOT EXISTS request_logs (
    request_id VARCHAR(64) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    gw_session_id VARCHAR(128),
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    stage SMALLINT NOT NULL DEFAULT 0,
    client_model VARCHAR(100),
    upstream_provider_id BIGINT,
    upstream_credential_id BIGINT,
    completion_tokens INTEGER,
    prompt_tokens INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    upstream_request_at TIMESTAMPTZ,
    upstream_response_at TIMESTAMPTZ,
    error TEXT,
    compression_strategy VARCHAR(50),
    compression_meta JSONB,
    PRIMARY KEY (request_id, created_at)
) PARTITION BY RANGE (created_at);

-- Create initial partitions
CREATE TABLE IF NOT EXISTS request_logs_2026_06 PARTITION OF request_logs
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

CREATE TABLE IF NOT EXISTS request_logs_2026_07 PARTITION OF request_logs
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

-- Create indexes
CREATE INDEX IF NOT EXISTS idx_status_stage ON request_logs (status, stage);
CREATE INDEX IF NOT EXISTS idx_session ON request_logs (gw_session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_tenant_created ON request_logs (tenant_id, created_at DESC);

-- Create request_bodies table for large outbound bodies
CREATE TABLE IF NOT EXISTS request_bodies (
    request_id VARCHAR(64) PRIMARY KEY,
    outbound_body TEXT,
    compression_meta JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Add comment
COMMENT ON TABLE request_logs IS 'Request WAL: synchronous initial log + async batch updates for request lifecycle';
COMMENT ON TABLE request_bodies IS 'Large outbound bodies separated for performance';
