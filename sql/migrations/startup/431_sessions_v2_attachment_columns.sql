-- Migration 431: Add attachment metadata columns to gateway.session_turns
-- 
-- Purpose: The TurnWriter code references attachment_count, 
-- attachment_total_bytes, and multimodal_types columns that were 
-- added to the TurnRecord struct but never reflected in the 
-- 430_sessions_v2_schema.sql DDL.
--
-- Author: llm-gateway-ops
-- Date: 2026-07-21
-- Status: PARALLEL

BEGIN;

ALTER TABLE gateway.session_turns 
    ADD COLUMN IF NOT EXISTS attachment_count INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS attachment_total_bytes BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS multimodal_types TEXT[];

COMMENT ON COLUMN gateway.session_turns.attachment_count IS 
    'Number of attachments in this turn (added in migration 431)';
COMMENT ON COLUMN gateway.session_turns.attachment_total_bytes IS 
    'Total bytes of all attachments in this turn (added in migration 431)';
COMMENT ON COLUMN gateway.session_turns.multimodal_types IS 
    'Multimodal content types present: image, audio, video, document (added in migration 431)';

COMMIT;
