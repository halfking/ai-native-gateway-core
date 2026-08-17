-- 531_request_journey_tenant_uniqueness.down.sql
BEGIN;

DROP INDEX IF EXISTS uq_state_transitions_legacy_request_seq;
DROP INDEX IF EXISTS uq_state_transitions_tenant_request_seq;

CREATE UNIQUE INDEX IF NOT EXISTS uq_state_transitions_request_seq
    ON request_state_transitions (request_id, seq);

COMMIT;
