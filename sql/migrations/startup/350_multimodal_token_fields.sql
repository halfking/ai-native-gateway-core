-- audit-ir-multimodal (2026-07-13): Add multimodal token fields to request_logs
-- for accurate billing of vision, audio, video, and provider-specific tokens.
--
-- Background: Previously only prompt_tokens, completion_tokens, cache_read_tokens,
-- cache_write_tokens, and reasoning_tokens were tracked. Multimodal requests
-- (vision, audio, video) had their token costs aggregated into prompt_tokens,
-- making it impossible to:
--   1. Break down billing by modality
--   2. Validate provider token counts
--   3. Generate detailed usage reports
--
-- This migration adds dedicated fields for each modality, enabling:
--   - Separate billing rates for text vs image vs audio
--   - Usage analytics by content type
--   - Detection of token counting anomalies

BEGIN;

-- Add multimodal token columns to request_logs
ALTER TABLE request_logs
  ADD COLUMN IF NOT EXISTS image_tokens INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS audio_tokens INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS video_tokens INT DEFAULT NULL,
  ADD COLUMN IF NOT EXISTS provider_tokens INT DEFAULT NULL;

-- Add comments for documentation
COMMENT ON COLUMN request_logs.image_tokens IS 'audit-ir-multimodal (2026-07-13): Vision input tokens (OpenAI image_tokens, Anthropic vision pricing)';
COMMENT ON COLUMN request_logs.audio_tokens IS 'audit-ir-multimodal (2026-07-13): Audio input/output tokens (GPT-4o Audio, Gemini 2.0 multimodal)';
COMMENT ON COLUMN request_logs.video_tokens IS 'audit-ir-multimodal (2026-07-13): Video input tokens (Gemini 2.0 Flash)';
COMMENT ON COLUMN request_logs.provider_tokens IS 'audit-ir-multimodal (2026-07-13): Provider-specific tokens (e.g., Doubao seed_token_usage)';

-- Add index for multimodal usage queries
-- Enables fast "show me all vision requests" or "total audio tokens this month"
CREATE INDEX IF NOT EXISTS idx_request_logs_multimodal_usage
  ON request_logs (tenant_id, created_at)
  WHERE image_tokens > 0 OR audio_tokens > 0 OR video_tokens > 0;

-- Add index for cache token queries (existing cache fields now have proper index)
CREATE INDEX IF NOT EXISTS idx_request_logs_cache_usage
  ON request_logs (tenant_id, created_at)
  WHERE cache_read_tokens > 0 OR cache_write_tokens > 0;

COMMIT;

-- Verification queries (commented out, for manual testing):
-- 
-- -- Check new columns exist
-- SELECT column_name, data_type, is_nullable 
-- FROM information_schema.columns 
-- WHERE table_name = 'request_logs' 
--   AND column_name IN ('image_tokens', 'audio_tokens', 'video_tokens', 'provider_tokens')
-- ORDER BY column_name;
--
-- -- Check indexes created
-- SELECT indexname, indexdef 
-- FROM pg_indexes 
-- WHERE tablename = 'request_logs' 
--   AND indexname LIKE '%multimodal%' OR indexname LIKE '%cache_usage%';
--
-- -- Sample query: multimodal usage breakdown
-- SELECT 
--   CASE 
--     WHEN image_tokens > 0 THEN 'vision'
--     WHEN audio_tokens > 0 THEN 'audio'
--     WHEN video_tokens > 0 THEN 'video'
--     ELSE 'text'
--   END AS modality,
--   COUNT(*) AS requests,
--   SUM(COALESCE(image_tokens, 0) + COALESCE(audio_tokens, 0) + COALESCE(video_tokens, 0)) AS multimodal_tokens,
--   SUM(prompt_tokens) AS total_prompt_tokens,
--   SUM(cost_usd) AS total_cost
-- FROM request_logs
-- WHERE tenant_id = 1 AND created_at >= NOW() - INTERVAL '7 days'
-- GROUP BY modality
-- ORDER BY total_cost DESC;
