-- Down migration 530: remove the additive RequestJourney contract.
-- Operator-gated: journey rows become legacy transition rows after rollback.

BEGIN;

DROP INDEX IF EXISTS idx_state_transitions_journey_node_recent;
DROP INDEX IF EXISTS idx_state_transitions_journey_model_recent;
DROP INDEX IF EXISTS idx_state_transitions_journey_recent;
DROP INDEX IF EXISTS uq_state_transitions_tenant_request_seq;

UPDATE request_state_transitions
SET transition_type = 'state'
WHERE transition_type IS NULL;

ALTER TABLE request_state_transitions
    DROP CONSTRAINT IF EXISTS request_state_transitions_node_health_status_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_observation_status_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_degraded_event_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_canceled_outcome_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_switch_fields_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_event_fields_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_provider_credential_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_http_status_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_outcome_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_attempt_ref_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_journey_stage_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_event_type_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_journey_no_metadata_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_journey_required_chk,
    DROP CONSTRAINT IF EXISTS request_state_transitions_event_kind_chk,
    DROP COLUMN IF EXISTS occurred_at,
    DROP COLUMN IF EXISTS node_health_status,
    DROP COLUMN IF EXISTS observation_status,
    DROP COLUMN IF EXISTS switch_reason,
    DROP COLUMN IF EXISTS retry_reason,
    DROP COLUMN IF EXISTS http_status,
    DROP COLUMN IF EXISTS error_kind,
    DROP COLUMN IF EXISTS outcome,
    DROP COLUMN IF EXISTS credential_id,
    DROP COLUMN IF EXISTS provider,
    DROP COLUMN IF EXISTS provider_id,
    DROP COLUMN IF EXISTS model,
    DROP COLUMN IF EXISTS attempt_no,
    DROP COLUMN IF EXISTS attempt_id,
    DROP COLUMN IF EXISTS to_credential_id,
    DROP COLUMN IF EXISTS from_credential_id,
    DROP COLUMN IF EXISTS to_model,
    DROP COLUMN IF EXISTS from_model,
    DROP COLUMN IF EXISTS resolved_model,
    DROP COLUMN IF EXISTS requested_model,
    DROP COLUMN IF EXISTS stage,
    DROP COLUMN IF EXISTS event_type,
    DROP COLUMN IF EXISTS gateway_instance_id,
    ALTER COLUMN transition_type SET NOT NULL;

COMMIT;
