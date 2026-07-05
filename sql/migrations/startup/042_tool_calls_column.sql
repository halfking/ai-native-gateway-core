-- Migration 042: Add tool_calls column to request_logs
-- 2026-06-23: Fix streaming tool_calls data loss after IR refactor
--
-- Problem: After migrating to IR-based streaming (commits 05bee9f9, 6e065a3e),
-- tool_calls data is captured in audit.StreamCapture.textContent as plain text
-- but NOT persisted as structured JSONB. The admin UI /api/logs endpoint returns
-- empty tool_calls[] for all streaming requests.
--
-- Root cause: request_logs table has no tool_calls column. The non-streaming
-- path stores tool_calls in response_body.choices[0].message.tool_calls, but
-- streaming path has no dedicated field.
--
-- Solution: Add tool_calls JSONB column to store structured tool call data
-- from both streaming and non-streaming paths.
--
-- Schema: tool_calls JSONB array of objects with shape:
-- [
--   {
--     "id": "call_abc123",
--     "type": "function",
--     "function": {
--       "name": "get_weather",
--       "arguments": "{\"location\":\"San Francisco\"}"
--     }
--   }
-- ]
--
-- This matches the OpenAI Chat Completions API format for tool_calls.

ALTER TABLE request_logs 
  ADD COLUMN IF NOT EXISTS tool_calls JSONB;

-- Index for querying requests with tool calls
CREATE INDEX IF NOT EXISTS idx_request_logs_tool_calls
  ON request_logs USING GIN (tool_calls)
  WHERE tool_calls IS NOT NULL AND tool_calls != '[]'::jsonb;

-- Index for tool call analytics (provider quality, model comparison)
CREATE INDEX IF NOT EXISTS idx_request_logs_provider_tool_calls
  ON request_logs (provider_id, ts DESC)
  WHERE tool_calls IS NOT NULL AND jsonb_array_length(tool_calls) > 0;

COMMENT ON COLUMN request_logs.tool_calls IS 
  'Structured tool calls from assistant message. OpenAI format: [{id, type, function: {name, arguments}}]. Populated for both streaming and non-streaming responses.';
