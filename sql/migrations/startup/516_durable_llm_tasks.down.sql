-- Migration 516 down: remove durable task schema and survival transition extensions.
-- Migration 515 seq and uq_state_transitions_request_seq are intentionally kept.

BEGIN;

DROP INDEX IF EXISTS idx_state_transitions_request_attempt;
ALTER TABLE request_state_transitions
    DROP CONSTRAINT IF EXISTS chk_request_state_transitions_attempt_no,
    DROP CONSTRAINT IF EXISTS chk_request_state_transitions_transition_type;

-- Survival rows cannot satisfy migration 511's original enum. They belong to
-- the 516 event vocabulary, so remove them before restoring the old constraint.
DELETE FROM request_state_transitions
WHERE transition_type LIKE 'survival\_%' ESCAPE '\';

ALTER TABLE request_state_transitions
    ADD CONSTRAINT request_state_transitions_transition_type_check
    CHECK (transition_type IN ('route', 'node_switch', 'retry', 'error', 'state'));
ALTER TABLE request_state_transitions
    DROP COLUMN IF EXISTS attempt_no;

DROP TABLE IF EXISTS durable_llm_tasks;

COMMIT;
