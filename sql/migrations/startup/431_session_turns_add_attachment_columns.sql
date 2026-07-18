-- Migration 431: Add attachment metadata columns to session_turns
-- Purpose: Support multimodal content tracking and attachment statistics
-- Date: 2026-07-19

BEGIN;

-- Add attachment metadata columns to session_turns
ALTER TABLE gateway.session_turns 
  ADD COLUMN IF NOT EXISTS attachment_count INTEGER DEFAULT 0,
  ADD COLUMN IF NOT EXISTS attachment_total_bytes BIGINT DEFAULT 0,
  ADD COLUMN IF NOT EXISTS multimodal_types TEXT[] DEFAULT '{}';

-- Add comments for documentation
COMMENT ON COLUMN gateway.session_turns.attachment_count IS 
  'Number of attachments in this turn (request + response combined)';

COMMENT ON COLUMN gateway.session_turns.attachment_total_bytes IS 
  'Total bytes of all attachments in this turn';

COMMENT ON COLUMN gateway.session_turns.multimodal_types IS 
  'Array of multimodal content types present in this turn: [image, audio, video, document]';

-- Create index for querying multimodal sessions
CREATE INDEX IF NOT EXISTS idx_session_turns_multimodal_types 
  ON gateway.session_turns USING gin(multimodal_types)
  WHERE multimodal_types != '{}';

-- Create index for attachment statistics queries
CREATE INDEX IF NOT EXISTS idx_session_turns_attachment_count 
  ON gateway.session_turns(attachment_count)
  WHERE attachment_count > 0;

COMMIT;
