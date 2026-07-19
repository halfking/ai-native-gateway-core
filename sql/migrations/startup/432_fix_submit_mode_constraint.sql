-- Migration 432: Fix submit_mode constraint to include attachment_only
-- 
-- Purpose: Extend CHECK constraint to allow 'attachment_only' submit mode
-- 
-- Context: Migration 431 added attachment metadata columns and Phase 2D
--          implemented attachment-only change detection, but the original
--          CHECK constraint in migration 430 only allowed 4 modes.
-- 
-- Impact: Without this fix, any attempt to write submit_mode='attachment_only'
--         will be rejected by PostgreSQL with a CHECK constraint violation.
-- 
-- Author: llm-gateway-ops
-- Date: 2026-07-19
-- Related: migrations/430 (original constraint), Phase 2D implementation

BEGIN;

-- Drop the existing constraint
ALTER TABLE gateway.session_turns 
  DROP CONSTRAINT IF EXISTS session_turns_submit_mode_check;

-- Re-create with the additional 'attachment_only' mode
ALTER TABLE gateway.session_turns 
  ADD CONSTRAINT session_turns_submit_mode_check 
  CHECK (submit_mode IN ('full', 'delta', 'snapshot', 'inferred_compressed', 'attachment_only'));

-- Verify
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint 
        WHERE conname = 'session_turns_submit_mode_check'
        AND conrelid = 'gateway.session_turns'::regclass
    ) THEN
        RAISE EXCEPTION 'Constraint session_turns_submit_mode_check not created';
    END IF;
    
    RAISE NOTICE '===== Migration 432 SUCCESSFUL =====';
    RAISE NOTICE 'submit_mode constraint now allows: full, delta, snapshot, inferred_compressed, attachment_only';
END;
$$;

COMMIT;
