-- Rollback migration 605.
-- Restores the previous predicate for compatibility with the prior schema.
-- The restored predicate is unsafe for JSON scalar values; use only for
-- emergency rollback while migration 605 is being investigated.

\set ON_ERROR_STOP on
BEGIN;

DROP INDEX IF EXISTS public.idx_request_logs_provider_tool_calls;

CREATE INDEX IF NOT EXISTS idx_request_logs_provider_tool_calls
    ON ONLY public.request_logs (provider_id, ts DESC)
    WHERE tool_calls IS NOT NULL
      AND jsonb_array_length(tool_calls) > 0;

COMMIT;
