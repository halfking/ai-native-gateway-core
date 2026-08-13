-- Migration 474 down: revert CHECK constraint + drop added indexes
--
-- Note: this restores the constraint to its pre-migration-474 state (4 modes
-- without attachment_only), and drops the indexes added by 474. Other
-- state (columns from other migrations) is left untouched.

BEGIN;

DROP INDEX IF EXISTS gateway.idx_session_turns_multimodal_types_gw;
DROP INDEX IF EXISTS gateway.idx_session_turns_attachment_count_gw;

ALTER TABLE public.session_turns
  DROP CONSTRAINT IF EXISTS session_turns_submit_mode_check;
ALTER TABLE public.session_turns
  ADD CONSTRAINT session_turns_submit_mode_check
  CHECK (submit_mode IN ('full', 'delta', 'snapshot', 'inferred_compressed'));

ALTER TABLE public.session_turns
  DROP CONSTRAINT IF EXISTS session_turns_submit_mode_check;
ALTER TABLE public.session_turns
  ADD CONSTRAINT session_turns_submit_mode_check
  CHECK (submit_mode IN ('full', 'delta', 'snapshot', 'inferred_compressed'));

COMMIT;