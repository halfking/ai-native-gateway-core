-- Migration 549: durable RequestJourney observation outbox and retry_at parity.
BEGIN;

ALTER TABLE request_state_transitions
    ADD COLUMN IF NOT EXISTS retry_at TIMESTAMPTZ;

COMMENT ON COLUMN request_state_transitions.retry_at IS
    'RequestJourney timed retry deadline; only retry_scheduled journey events may set it.';

ALTER TABLE request_state_transitions
    DROP CONSTRAINT IF EXISTS request_state_transitions_retry_at_event_chk,
    ADD CONSTRAINT request_state_transitions_retry_at_event_chk CHECK (
        event_type IS NULL OR retry_at IS NULL OR event_type = 'retry_scheduled'
    );

CREATE INDEX IF NOT EXISTS idx_state_transitions_journey_retry_at
    ON request_state_transitions (tenant_id, retry_at)
    WHERE event_type = 'retry_scheduled' AND retry_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS request_journey_observation_outbox (
    id             BIGSERIAL PRIMARY KEY,
    tenant_id      TEXT NOT NULL,
    request_id     TEXT NOT NULL,
    seq            BIGINT NOT NULL,
    payload        JSONB NOT NULL,
    payload_hash   TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending',
    attempts       INTEGER NOT NULL DEFAULT 0,
    next_retry_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claim_owner    TEXT,
    claim_until    TIMESTAMPTZ,
    last_error     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT request_journey_observation_outbox_identity_uq
        UNIQUE (tenant_id, request_id, seq),
    CONSTRAINT request_journey_observation_outbox_seq_chk CHECK (seq > 0),
    CONSTRAINT request_journey_observation_outbox_attempts_chk CHECK (attempts >= 0),
    CONSTRAINT request_journey_observation_outbox_status_chk
        CHECK (status IN ('pending', 'processing', 'failed'))
);

CREATE INDEX IF NOT EXISTS idx_request_journey_observation_outbox_due
    ON request_journey_observation_outbox (next_retry_at, created_at)
    WHERE status IN ('pending', 'failed');
CREATE INDEX IF NOT EXISTS idx_request_journey_observation_outbox_lease
    ON request_journey_observation_outbox (claim_until, created_at)
    WHERE status = 'processing';
CREATE INDEX IF NOT EXISTS idx_request_journey_observation_outbox_tenant
    ON request_journey_observation_outbox (tenant_id, created_at);

ALTER TABLE request_journey_observation_outbox ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS request_journey_observation_outbox_tenant_isolation
    ON request_journey_observation_outbox;
CREATE POLICY request_journey_observation_outbox_tenant_isolation
    ON request_journey_observation_outbox
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
DROP POLICY IF EXISTS request_journey_observation_outbox_super_admin_bypass
    ON request_journey_observation_outbox;
CREATE POLICY request_journey_observation_outbox_super_admin_bypass
    ON request_journey_observation_outbox
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMIT;
