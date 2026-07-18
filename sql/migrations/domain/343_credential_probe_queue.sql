-- Migration 343: persistent credential+model self-check queue.
-- Purpose: durable deduplication, 300-second TTL, and lease-based workers.
-- Rollback: sql/migrations/domain/343_credential_probe_queue.down.sql

BEGIN;

CREATE TABLE IF NOT EXISTS credential_probe_queue (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    credential_id BIGINT NOT NULL,
    provider_id BIGINT,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    canonical_model TEXT,
    raw_model TEXT NOT NULL,
    outbound_model TEXT,
    probe_command TEXT NOT NULL,
    probe_mode TEXT NOT NULL DEFAULT 'single',
    priority SMALLINT NOT NULL DEFAULT 50,
    status TEXT NOT NULL DEFAULT 'ready',
    reason_code TEXT,
    reason_detail TEXT,
    attempt INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 1,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ,
    source TEXT NOT NULL DEFAULT 'request_failure',
    source_event_id TEXT,
    parent_request_id TEXT,
    dedup_key TEXT NOT NULL,
    result_http_status INT,
    result_latency_ms INT,
    result_body_preview TEXT,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '300 seconds'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT credential_probe_queue_status_check CHECK (status IN ('ready', 'running', 'success', 'failed', 'expired', 'cancelled')),
    CONSTRAINT credential_probe_queue_mode_check CHECK (probe_mode IN ('single', 'multi_round')),
    CONSTRAINT credential_probe_queue_attempt_check CHECK (attempt >= 0 AND max_attempts > 0),
    CONSTRAINT credential_probe_queue_source_check CHECK (source IN ('request_failure', 'periodic', 'external_async', 'admin'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_credential_probe_queue_dedup_active
    ON credential_probe_queue (dedup_key)
    WHERE status IN ('ready', 'running');

CREATE UNIQUE INDEX IF NOT EXISTS uq_credential_probe_queue_source_event
    ON credential_probe_queue (source_event_id)
    WHERE source_event_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_credential_probe_queue_claim
    ON credential_probe_queue (priority DESC, next_run_at, id)
    WHERE status = 'ready';

CREATE INDEX IF NOT EXISTS idx_credential_probe_queue_expiry
    ON credential_probe_queue (expires_at)
    WHERE status IN ('ready', 'running');

COMMENT ON TABLE credential_probe_queue IS
    '343: durable credential+model probe tasks with 300-second TTL and worker leases.';

COMMIT;
