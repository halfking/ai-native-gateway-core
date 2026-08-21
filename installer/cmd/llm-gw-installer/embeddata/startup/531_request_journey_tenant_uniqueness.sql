-- 531_request_journey_tenant_uniqueness.sql
-- Move replay uniqueness to the correct scope after the deployed 530 contract.
BEGIN;

DROP INDEX IF EXISTS uq_state_transitions_request_seq;

CREATE UNIQUE INDEX IF NOT EXISTS uq_state_transitions_legacy_request_seq
    ON request_state_transitions (request_id, seq)
    WHERE event_type IS NULL;

-- Journey request IDs are tenant-scoped; 530 already created this index.
CREATE UNIQUE INDEX IF NOT EXISTS uq_state_transitions_tenant_request_seq
    ON request_state_transitions (tenant_id, request_id, seq)
    WHERE event_type IS NOT NULL;

COMMIT;
