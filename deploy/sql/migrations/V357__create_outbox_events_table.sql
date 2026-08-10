-- V357__create_outbox_events_table.sql
-- Phase 2 Step 1: Create outbox_events table for Gateway → ASM event delivery
-- Refs: docs/修订0811/06-下一阶段实施计划.md WP2
--       docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md §2

-- Purpose:
-- This table implements the durable outbox pattern for cross-repo event delivery.
-- Events are written to this table in the same transaction as the business facts,
-- ensuring at-least-once delivery semantics.
--
-- Design constraints:
--   - event_id must be fixed across retries (idempotency)
--   - aggregate_version must be monotonically increasing per aggregate
--   - payload is the raw JSON bytes used for HMAC signature
--   - status tracks delivery state: pending → sent | failed
--   - next_retry_at enables exponential backoff

CREATE TABLE IF NOT EXISTS outbox_events (
    id BIGSERIAL PRIMARY KEY,
    
    -- Event envelope (matches EventEnvelope in test/events/contract/)
    event_id TEXT NOT NULL UNIQUE,
    event_type TEXT NOT NULL,
    schema_version INT NOT NULL DEFAULT 1,
    tenant_id TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,  -- session_id for session events
    aggregate_version INT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    
    -- Payload (raw JSON bytes for signature)
    payload JSONB NOT NULL,
    
    -- Delivery state
    status TEXT NOT NULL DEFAULT 'pending',  -- pending | sent | failed | dlq
    attempts INT NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    last_error TEXT,
    next_retry_at TIMESTAMPTZ,
    
    -- Audit
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- Constraints
    CONSTRAINT outbox_events_status_check 
        CHECK (status IN ('pending', 'sent', 'failed', 'dlq')),
    CONSTRAINT outbox_events_version_positive 
        CHECK (aggregate_version > 0),
    CONSTRAINT outbox_events_attempts_non_negative 
        CHECK (attempts >= 0)
);

-- Index for dispatcher polling (primary query pattern)
-- Query: SELECT * FROM outbox_events 
--        WHERE status IN ('pending', 'failed') 
--        AND (next_retry_at IS NULL OR next_retry_at <= NOW())
--        ORDER BY created_at LIMIT 100
CREATE INDEX idx_outbox_events_dispatch 
    ON outbox_events(status, next_retry_at, created_at)
    WHERE status IN ('pending', 'failed');

-- Index for aggregate version ordering (duplicate/stale detection)
-- Query: SELECT MAX(aggregate_version) FROM outbox_events 
--        WHERE tenant_id = ? AND aggregate_id = ?
CREATE INDEX idx_outbox_events_aggregate 
    ON outbox_events(tenant_id, aggregate_id, aggregate_version);

-- Index for event_id uniqueness lookups (idempotency)
-- Already covered by UNIQUE constraint, but explicit for documentation
-- Query: SELECT * FROM outbox_events WHERE event_id = ?

-- Index for tenant-scoped queries (observability)
CREATE INDEX idx_outbox_events_tenant_occurred 
    ON outbox_events(tenant_id, occurred_at DESC);

-- Index for failed events monitoring
CREATE INDEX idx_outbox_events_failed_attempts 
    ON outbox_events(status, attempts, last_attempt_at)
    WHERE status IN ('failed', 'dlq');

-- Updated_at trigger
CREATE OR REPLACE FUNCTION update_outbox_events_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trigger_outbox_events_updated_at
    BEFORE UPDATE ON outbox_events
    FOR EACH ROW
    EXECUTE FUNCTION update_outbox_events_updated_at();

-- Comments for documentation
COMMENT ON TABLE outbox_events IS 
    'Durable outbox for Gateway → ASM event delivery. Events are written here in the same transaction as business facts, ensuring at-least-once delivery.';

COMMENT ON COLUMN outbox_events.event_id IS 
    'Globally unique event identifier. Must remain fixed across retries for idempotency.';

COMMENT ON COLUMN outbox_events.aggregate_id IS 
    'Aggregate root identifier (e.g., session_id for session events). Used for version ordering.';

COMMENT ON COLUMN outbox_events.aggregate_version IS 
    'Monotonically increasing version number per aggregate. ASM rejects out-of-order events.';

COMMENT ON COLUMN outbox_events.payload IS 
    'Raw JSON payload. These bytes are used for HMAC-SHA256 signature. Do not modify after insertion.';

COMMENT ON COLUMN outbox_events.status IS 
    'Delivery state: pending (not yet sent), sent (delivered), failed (retryable), dlq (dead letter)';

COMMENT ON COLUMN outbox_events.next_retry_at IS 
    'Timestamp for next retry attempt. NULL means retry immediately. Set by dispatcher using exponential backoff.';

-- Migration metadata
COMMENT ON TABLE outbox_events IS 
    'V357: Created 2026-08-11 for Phase 2 WP2. Refs: docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md';
