-- Migration 000: Base tables foundation
-- Creates core tables that other migrations depend on
-- Must run first before any other startup migrations

BEGIN;

-- Request logs base table (minimal structure, extended by later migrations)
CREATE TABLE IF NOT EXISTS request_logs (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id TEXT,
    session_id TEXT,
    client_model TEXT,
    provider TEXT,
    upstream_model TEXT,
    status_code INTEGER,
    error_message TEXT,
    latency_ms INTEGER,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    total_tokens INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_request_logs_ts ON request_logs (ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_tenant_ts ON request_logs (tenant_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_session ON request_logs (session_id, ts DESC);

COMMENT ON TABLE request_logs IS 'Core request logging table (base structure, extended by later migrations)';

COMMIT;
