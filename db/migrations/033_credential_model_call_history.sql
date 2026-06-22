-- Migration 033: credential_model_call_history table for sliding window health tracking
-- Created: 2026-06-22
-- Purpose: Record per-credential-model call history for intelligent availability tracking

-- Main table: 1-minute window aggregation
CREATE TABLE IF NOT EXISTS credential_model_call_history (
    credential_id BIGINT NOT NULL,
    raw_model TEXT NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    
    -- Call counts
    total_calls INT NOT NULL DEFAULT 0,
    success_calls INT NOT NULL DEFAULT 0,
    failed_calls INT NOT NULL DEFAULT 0,
    
    -- Latency metrics
    avg_latency_ms NUMERIC(8,2),
    p95_latency_ms INT,
    p99_latency_ms INT,
    
    -- Error type distribution
    error_rate_limit_count INT NOT NULL DEFAULT 0,    -- 429 errors
    error_quota_count INT NOT NULL DEFAULT 0,          -- quota exhausted
    error_concurrent_count INT NOT NULL DEFAULT 0,     -- 503 overload
    error_network_count INT NOT NULL DEFAULT 0,        -- network failures
    error_auth_count INT NOT NULL DEFAULT 0,           -- 401/403
    error_other_count INT NOT NULL DEFAULT 0,          -- other errors
    
    -- Concurrency metrics
    avg_concurrent NUMERIC(5,2),
    peak_concurrent INT,
    
    -- Metadata
    created_at TIMESTAMPTZ DEFAULT now(),
    
    PRIMARY KEY (credential_id, raw_model, window_start)
);

-- Convert to TimescaleDB hypertable (1-day chunks, 7-day retention)
SELECT create_hypertable(
    'credential_model_call_history',
    'window_start',
    chunk_time_interval => INTERVAL '1 day',
    if_not_exists => TRUE
);

-- Retention policy: drop chunks older than 7 days
SELECT add_retention_policy(
    'credential_model_call_history',
    INTERVAL '7 days',
    if_not_exists => TRUE
);

-- Indexes for common queries
CREATE INDEX IF NOT EXISTS idx_call_history_cred_time 
    ON credential_model_call_history (credential_id, window_start DESC);

CREATE INDEX IF NOT EXISTS idx_call_history_model_time 
    ON credential_model_call_history (raw_model, window_start DESC);

CREATE INDEX IF NOT EXISTS idx_call_history_errors 
    ON credential_model_call_history (credential_id, raw_model, window_start DESC)
    WHERE error_rate_limit_count > 0 OR error_concurrent_count > 0;

-- Comments
COMMENT ON TABLE credential_model_call_history IS 
    'Aggregated call history per (credential, model) in 1-minute windows. Used for intelligent availability tracking, continuous failure detection, and concurrency auto-tuning.';

COMMENT ON COLUMN credential_model_call_history.error_rate_limit_count IS 
    '429 rate limit errors - triggers concurrency reduction';

COMMENT ON COLUMN credential_model_call_history.error_concurrent_count IS 
    '503 concurrent overload errors - triggers concurrency reduction';

COMMENT ON COLUMN credential_model_call_history.avg_concurrent IS 
    'Average concurrent requests in this window - used for auto-scaleup';

COMMENT ON COLUMN credential_model_call_history.peak_concurrent IS 
    'Peak concurrent requests in this window - used for capacity planning';
