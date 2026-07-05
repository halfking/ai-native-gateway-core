-- Request WAL (Write-Ahead Log) for request lifecycle tracking
-- 2026-06-22: Phase 1 implementation
-- NOTE: Uses SEPARATE table name (request_wal, not request_logs) to avoid
-- conflicting with the existing telemetry request_logs table.

-- Create partitioned request_wal table
CREATE TABLE IF NOT EXISTS request_wal (
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
CREATE TABLE IF NOT EXISTS request_wal_2026_06 PARTITION OF request_wal
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

CREATE TABLE IF NOT EXISTS request_wal_2026_07 PARTITION OF request_wal
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

-- Create indexes
CREATE INDEX IF NOT EXISTS idx_wal_status_stage ON request_wal (status, stage);
CREATE INDEX IF NOT EXISTS idx_wal_session ON request_wal (gw_session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_wal_tenant_created ON request_wal (tenant_id, created_at DESC);

-- Create request_wal_bodies table for large outbound bodies
CREATE TABLE IF NOT EXISTS request_wal_bodies (
    request_id VARCHAR(64) PRIMARY KEY,
    outbound_body TEXT,
    compression_meta JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Add comment
COMMENT ON TABLE request_wal IS 'Request WAL: synchronous initial log + async batch updates for request lifecycle';
COMMENT ON TABLE request_wal_bodies IS 'Large outbound bodies separated for performance';
