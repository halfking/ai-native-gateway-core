-- Migration 432 Rollback: Restore original submit_mode constraint
-- 
-- Purpose: Remove 'attachment_only' from allowed submit modes
-- 
-- Warning: This will fail if any existing rows have submit_mode='attachment_only'.
--          Clean up such rows before rolling back.
-- 
-- Author: llm-gateway-ops
-- Date: 2026-07-19

BEGIN;

-- Drop the extended constraint
ALTER TABLE public.session_turns 
  DROP CONSTRAINT IF EXISTS session_turns_submit_mode_check;

-- Restore original constraint (4 modes only)
ALTER TABLE public.session_turns 
  ADD CONSTRAINT session_turns_submit_mode_check 
  CHECK (submit_mode IN ('full', 'delta', 'snapshot', 'inferred_compressed'));

-- Verify
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint 
        WHERE conname = 'session_turns_submit_mode_check'
        AND conrelid = 'public.session_turns'::regclass
    ) THEN
        RAISE EXCEPTION 'Constraint session_turns_submit_mode_check not created';
    END IF;
    
    RAISE NOTICE '===== Migration 432 ROLLBACK SUCCESSFUL =====';
    RAISE NOTICE 'submit_mode constraint restored to: full, delta, snapshot, inferred_compressed';
END;
$$;

COMMIT;
