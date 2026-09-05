-- Down migration 552: remove durable RequestJourney observation persistence.
BEGIN;

DO $$
DECLARE
    v_outbox_rows BIGINT;
BEGIN
    IF to_regclass('public.request_journey_observation_outbox') IS NOT NULL THEN
        LOCK TABLE public.request_journey_observation_outbox IN ACCESS EXCLUSIVE MODE;
        SELECT count(*) INTO v_outbox_rows FROM public.request_journey_observation_outbox;
        IF v_outbox_rows <> 0 THEN
            RAISE EXCEPTION 'Migration 552 down refused: public.request_journey_observation_outbox contains % rows; drain durable observations before retrying',
                v_outbox_rows;
        END IF;
    END IF;
END $$;

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
