-- Migration 349: allow the integrity probe planner to enqueue durable tasks.
BEGIN;

ALTER TABLE credential_probe_queue
    DROP CONSTRAINT IF EXISTS credential_probe_queue_source_check;
ALTER TABLE credential_probe_queue
    ADD CONSTRAINT credential_probe_queue_source_check CHECK (
        source IN ('request_failure', 'periodic', 'external_async', 'admin', 'integrity_probe_planner')
    );

COMMIT;
