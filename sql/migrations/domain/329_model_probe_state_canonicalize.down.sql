-- Migration 329 (down): drop the state CHECK constraint introduced by 329.
-- The state backfill is irreversible without losing audit history, so
-- down-migration only relaxes the constraint.

BEGIN;

ALTER TABLE model_probe_state
    DROP CONSTRAINT IF EXISTS model_probe_state_state_check;

COMMIT;
