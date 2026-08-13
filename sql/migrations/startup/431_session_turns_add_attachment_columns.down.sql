-- Migration 431 Down: Remove attachment metadata columns from session_turns
-- Purpose: Rollback migration 431
-- Date: 2026-07-19

BEGIN;

-- Drop indexes
DROP INDEX IF EXISTS gateway.idx_session_turns_multimodal_types;
DROP INDEX IF EXISTS gateway.idx_session_turns_attachment_count;

-- Remove attachment metadata columns
ALTER TABLE public.session_turns 
  DROP COLUMN IF EXISTS attachment_count,
  DROP COLUMN IF EXISTS attachment_total_bytes,
  DROP COLUMN IF EXISTS multimodal_types;

COMMIT;
