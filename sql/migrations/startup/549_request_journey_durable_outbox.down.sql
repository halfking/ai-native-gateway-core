-- Down migration 549: remove durable RequestJourney observation persistence.
BEGIN;

DROP POLICY IF EXISTS request_journey_observation_outbox_super_admin_bypass
    ON request_journey_observation_outbox;
DROP POLICY IF EXISTS request_journey_observation_outbox_tenant_isolation
    ON request_journey_observation_outbox;
DROP TABLE IF EXISTS request_journey_observation_outbox;

DROP INDEX IF EXISTS idx_state_transitions_journey_retry_at;
ALTER TABLE request_state_transitions
    DROP CONSTRAINT IF EXISTS request_state_transitions_retry_at_event_chk,
    DROP COLUMN IF EXISTS retry_at;

COMMIT;
