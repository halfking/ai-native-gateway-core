-- Migration 529: RequestJourney shared observation contract (Wave 0).
--
-- Evolves request_state_transitions instead of creating another large event
-- table. Existing transition rows and writers remain valid because journey
-- columns are nullable and journey-only constraints are gated by event_type.
-- RequestJourney rows are content-free: they use explicit diagnostic columns,
-- must leave legacy metadata NULL, and have no body/header/secret columns.

BEGIN;

ALTER TABLE request_state_transitions
    ALTER COLUMN transition_type DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS gateway_instance_id TEXT,
    ADD COLUMN IF NOT EXISTS event_type TEXT,
    ADD COLUMN IF NOT EXISTS stage TEXT,
    ADD COLUMN IF NOT EXISTS requested_model TEXT,
    ADD COLUMN IF NOT EXISTS resolved_model TEXT,
    ADD COLUMN IF NOT EXISTS from_model TEXT,
    ADD COLUMN IF NOT EXISTS to_model TEXT,
    ADD COLUMN IF NOT EXISTS from_credential_id BIGINT,
    ADD COLUMN IF NOT EXISTS to_credential_id BIGINT,
    ADD COLUMN IF NOT EXISTS attempt_id TEXT,
    ADD COLUMN IF NOT EXISTS attempt_no INTEGER,
    ADD COLUMN IF NOT EXISTS model TEXT,
    ADD COLUMN IF NOT EXISTS provider_id BIGINT,
    ADD COLUMN IF NOT EXISTS provider TEXT,
    ADD COLUMN IF NOT EXISTS credential_id BIGINT,
    ADD COLUMN IF NOT EXISTS outcome TEXT,
    ADD COLUMN IF NOT EXISTS error_kind TEXT,
    ADD COLUMN IF NOT EXISTS http_status INTEGER,
    ADD COLUMN IF NOT EXISTS retry_reason TEXT,
    ADD COLUMN IF NOT EXISTS switch_reason TEXT,
    ADD COLUMN IF NOT EXISTS observation_status TEXT,
    ADD COLUMN IF NOT EXISTS node_health_status TEXT,
    ADD COLUMN IF NOT EXISTS occurred_at TIMESTAMPTZ;

COMMENT ON COLUMN request_state_transitions.gateway_instance_id IS
    'RequestJourney producer instance; required for journey rows.';
COMMENT ON COLUMN request_state_transitions.event_type IS
    'Wave 0 RequestJourney event discriminator. NULL identifies a legacy transition row.';
COMMENT ON COLUMN request_state_transitions.seq IS
    'Stable positive sequence within tenant_id + request_id for RequestJourney rows; legacy rows retain migration 515 replay semantics.';
COMMENT ON COLUMN request_state_transitions.occurred_at IS
    'Producer timestamp for the observed event; created_at remains the database ingestion timestamp.';
COMMENT ON COLUMN request_state_transitions.observation_status IS
    'Observation completeness: complete or observation_degraded.';

ALTER TABLE request_state_transitions
    DROP CONSTRAINT IF EXISTS request_state_transitions_event_kind_chk,
    ADD CONSTRAINT request_state_transitions_event_kind_chk CHECK (
        transition_type IS NOT NULL OR event_type IS NOT NULL
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_journey_required_chk,
    ADD CONSTRAINT request_state_transitions_journey_required_chk CHECK (
        event_type IS NULL OR (
            length(btrim(tenant_id)) > 0
            AND gateway_instance_id IS NOT NULL
            AND length(btrim(gateway_instance_id)) > 0
            AND length(btrim(request_id)) > 0
            AND seq IS NOT NULL
            AND seq > 0
            AND stage IS NOT NULL
            AND occurred_at IS NOT NULL
            AND observation_status IS NOT NULL
        )
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_journey_no_metadata_chk,
    ADD CONSTRAINT request_state_transitions_journey_no_metadata_chk CHECK (
        event_type IS NULL OR (
            metadata IS NULL AND from_state IS NULL AND to_state IS NULL
        )
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_event_type_chk,
    ADD CONSTRAINT request_state_transitions_event_type_chk CHECK (
        event_type IS NULL OR event_type IN (
            'request_received',
            'route_resolved',
            'model_enqueued',
            'credential_selected',
            'node_enqueued',
            'node_selected',
            'attempt_started',
            'first_byte',
            'attempt_succeeded',
            'attempt_failed',
            'retry_scheduled',
            'node_switched',
            'model_switched',
            'request_succeeded',
            'request_failed',
            'request_canceled',
            'observation_degraded'
        )
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_journey_stage_chk,
    ADD CONSTRAINT request_state_transitions_journey_stage_chk CHECK (
        stage IS NULL OR stage IN (
            'received', 'routing', 'model_queue', 'credential_queue',
            'node_selection', 'upstream', 'streaming', 'retrying', 'terminal'
        )
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_attempt_ref_chk,
    ADD CONSTRAINT request_state_transitions_attempt_ref_chk CHECK (
        event_type IS NULL OR (
            (attempt_id IS NULL AND attempt_no IS NULL)
            OR (attempt_id IS NOT NULL AND length(btrim(attempt_id)) > 0
                AND attempt_no IS NOT NULL AND attempt_no > 0)
        )
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_outcome_chk,
    ADD CONSTRAINT request_state_transitions_outcome_chk CHECK (
        outcome IS NULL OR outcome IN ('success', 'failure', 'canceled')
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_http_status_chk,
    ADD CONSTRAINT request_state_transitions_http_status_chk CHECK (
        http_status IS NULL OR http_status BETWEEN 100 AND 599
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_provider_credential_chk,
    ADD CONSTRAINT request_state_transitions_provider_credential_chk CHECK (
        (provider_id IS NULL OR provider_id > 0)
        AND (credential_id IS NULL OR credential_id > 0)
        AND (from_credential_id IS NULL OR from_credential_id > 0)
        AND (to_credential_id IS NULL OR to_credential_id > 0)
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_event_fields_chk,
    ADD CONSTRAINT request_state_transitions_event_fields_chk CHECK (
        event_type IS NULL
        OR event_type NOT IN ('credential_selected', 'node_enqueued', 'node_selected')
        OR (credential_id IS NOT NULL AND credential_id > 0)
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_switch_fields_chk,
    ADD CONSTRAINT request_state_transitions_switch_fields_chk CHECK (
        event_type IS NULL
        OR (event_type <> 'node_switched' OR (
            from_credential_id IS NOT NULL AND from_credential_id > 0
            AND to_credential_id IS NOT NULL AND to_credential_id > 0
        ))
        AND (event_type <> 'model_switched' OR (
            from_model IS NOT NULL AND length(btrim(from_model)) > 0
            AND to_model IS NOT NULL AND length(btrim(to_model)) > 0
        ))
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_canceled_outcome_chk,
    ADD CONSTRAINT request_state_transitions_canceled_outcome_chk CHECK (
        event_type IS NULL OR event_type <> 'request_canceled'
        OR (outcome IS NOT NULL AND outcome = 'canceled')
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_degraded_event_chk,
    ADD CONSTRAINT request_state_transitions_degraded_event_chk CHECK (
        event_type IS NULL OR event_type <> 'observation_degraded'
        OR (observation_status IS NOT NULL AND observation_status = 'observation_degraded')
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_observation_status_chk,
    ADD CONSTRAINT request_state_transitions_observation_status_chk CHECK (
        observation_status IS NULL OR observation_status IN ('complete', 'observation_degraded')
    ),
    DROP CONSTRAINT IF EXISTS request_state_transitions_node_health_status_chk,
    ADD CONSTRAINT request_state_transitions_node_health_status_chk CHECK (
        node_health_status IS NULL OR node_health_status IN (
            'unknown', 'healthy', 'suspect', 'degraded', 'cooling', 'probing',
            'recovering', 'quarantined', 'disabled', 'unhealthy'
        )
    );

-- Keep migration 515's (request_id, seq) replay index for existing writers.
-- The tenant-scoped key freezes the RequestJourney lookup/idempotency contract.
CREATE UNIQUE INDEX IF NOT EXISTS uq_state_transitions_tenant_request_seq
    ON request_state_transitions (tenant_id, request_id, seq)
    WHERE event_type IS NOT NULL;

-- FIFO query paths: recent total requests, per model, and per routing node.
CREATE INDEX IF NOT EXISTS idx_state_transitions_journey_recent
    ON request_state_transitions (tenant_id, occurred_at DESC, request_id)
    WHERE event_type IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_state_transitions_journey_model_recent
    ON request_state_transitions (tenant_id, resolved_model, occurred_at DESC, request_id)
    WHERE event_type IS NOT NULL AND resolved_model IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_state_transitions_journey_node_recent
    ON request_state_transitions (
        tenant_id, resolved_model, provider_id, credential_id, occurred_at DESC, request_id
    )
    WHERE event_type IS NOT NULL
        AND resolved_model IS NOT NULL
        AND provider_id IS NOT NULL
        AND credential_id IS NOT NULL;

COMMIT;
