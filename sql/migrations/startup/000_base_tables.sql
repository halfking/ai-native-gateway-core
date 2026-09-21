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
-- Guarded: a schema synced from 252 (or any pre-existing request_logs that
-- predates the session_id column) lacks the column and would fail
-- CREATE INDEX with "column does not exist". The strict runner normally
-- skips 000 on a populated ledger, but keep the guard so partial replays
-- never trip the index creation.
DO $do$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'request_logs' AND column_name = 'session_id'
  ) THEN
    CREATE INDEX IF NOT EXISTS idx_request_logs_session ON request_logs (session_id, ts DESC);
  END IF;
END
$do$;

COMMENT ON TABLE request_logs IS 'Core request logging table (base structure, extended by later migrations)';

COMMIT;
