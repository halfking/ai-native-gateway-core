-- Migration 550: durable RequestJourney observation outbox and retry_at parity.
-- The outbox persists content-free observation events before asynchronous
-- PostgreSQL/Redis projection. It is distinct from durable task execution and
-- Gateway-to-ASM delivery.
BEGIN;

ALTER TABLE request_state_transitions
    ADD COLUMN IF NOT EXISTS retry_at TIMESTAMPTZ;

COMMENT ON COLUMN request_state_transitions.retry_at IS
    'RequestJourney timed retry deadline; only retry_scheduled journey events may set it.';

ALTER TABLE request_state_transitions
    DROP CONSTRAINT IF EXISTS request_state_transitions_retry_at_event_chk,
    ADD CONSTRAINT request_state_transitions_retry_at_event_chk CHECK (
        retry_at IS NULL OR event_type = 'retry_scheduled'
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
    claim_owner          TEXT,
    claim_until          TIMESTAMPTZ,
    claim_fencing_token  BIGINT NOT NULL DEFAULT 0,
    last_error           TEXT NOT NULL DEFAULT '',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT request_journey_observation_outbox_identity_uq
        UNIQUE (tenant_id, request_id, seq),
    CONSTRAINT request_journey_observation_outbox_seq_chk CHECK (seq > 0),
    CONSTRAINT request_journey_observation_outbox_attempts_chk CHECK (attempts >= 0),
    CONSTRAINT request_journey_observation_outbox_claim_fence_chk
        CHECK (claim_fencing_token >= 0),
    CONSTRAINT request_journey_observation_outbox_status_chk
        CHECK (status IN ('pending', 'processing', 'failed')),
    CONSTRAINT request_journey_observation_outbox_processing_lease_chk CHECK (
        status <> 'processing' OR (claim_owner IS NOT NULL AND claim_until IS NOT NULL)
    )
);

ALTER TABLE request_journey_observation_outbox
    ADD COLUMN IF NOT EXISTS claim_fencing_token BIGINT NOT NULL DEFAULT 0;
ALTER TABLE request_journey_observation_outbox
    DROP CONSTRAINT IF EXISTS request_journey_observation_outbox_claim_fence_chk,
    ADD CONSTRAINT request_journey_observation_outbox_claim_fence_chk
        CHECK (claim_fencing_token >= 0),
    DROP CONSTRAINT IF EXISTS request_journey_observation_outbox_processing_lease_chk,
    ADD CONSTRAINT request_journey_observation_outbox_processing_lease_chk CHECK (
        status <> 'processing' OR (claim_owner IS NOT NULL AND claim_until IS NOT NULL)
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
ALTER TABLE request_journey_observation_outbox FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS request_journey_observation_outbox_tenant_isolation
    ON request_journey_observation_outbox;
CREATE POLICY request_journey_observation_outbox_tenant_isolation
    ON request_journey_observation_outbox
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::TEXT);
DROP POLICY IF EXISTS request_journey_observation_outbox_super_admin_bypass
    ON request_journey_observation_outbox;
CREATE POLICY request_journey_observation_outbox_super_admin_bypass
    ON request_journey_observation_outbox
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMIT;
