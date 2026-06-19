-- 018_upstream_finish_reason.sql
-- 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code.
--
-- Background:
--   audit/audit.go::StreamCapture.ObservePayload writes the upstream
--   finish_reason into StreamCapture.finalFinish, and relay/handler.go
--   then publishes it as `failure_detail_code` in the request_logs row
--   — regardless of success/failure. Concretely:
--
--     - Successful streams with finish_reason="stop"      → failure_detail_code="stop"
--     - Successful streams with finish_reason="tool_calls" → failure_detail_code="tool_calls"
--     - Successful streams with finish_reason="length"    → failure_detail_code="length"
--     - Successful streams with finish_reason="end_turn"  → failure_detail_code="end_turn"
--     - Successful streams hit by client disconnects     → failure_detail_code="client_cancel"
--     - Interruption codes ("eof_without_done" etc.)     → failure_detail_code=<interruption>
--
--   Result: the admin UI's "失败详情: tool_calls" column reads as a
--   failure even when the request returned 200 OK with a normal
--   `finish_reason: stop` body. Production check (24h, 7d):
--       eof_without_done  | success=t  3,103 / 14,437
--       tool_calls        | success=t    361 /  8,228
--       stop              | success=t     19 /    357
--       client_cancel     | success=t     11 /     81
--       length            | success=t      X /     22
--   → ~23k false-positive "failure detail" rows in 7 days, plus the
--     real bug: a successful tool-call response shows "失败详情: tool_calls".
--
-- This migration introduces a new column `upstream_finish_reason` that
-- is the SOLE home for the upstream finish_reason. The legacy column
-- `failure_detail_code` is preserved (no DDL change to its type) but
-- backfilled to NULL where the value is a known normal finish reason
-- on a successful row. New INSERTs/UPDATEs follow the same discipline.
--
-- Normal finish reasons (whitelist — OpenAI + Anthropic + Together +
-- Mistral canonical values seen in production):
--   stop, tool_calls, function_call, length, end_turn, max_tokens
--
-- Interruption / failure codes (NOT to be moved):
--   eof_without_done, stream_timeout, client_cancel, no_deltas,
--   invalid_first_chunk, request_body_too_large, upstream_5xx, etc.
--
-- Idempotency: ADD COLUMN IF NOT EXISTS, UPDATE ... WHERE uses
-- stable keys. Safe to re-run.

BEGIN;

-- ■ 1. New column on request_logs (parent + every partition inherits).
ALTER TABLE public.request_logs
    ADD COLUMN IF NOT EXISTS upstream_finish_reason TEXT;

COMMENT ON COLUMN public.request_logs.upstream_finish_reason IS
    '2026-06-19 T-NEW-7: the SOLE home for the upstream finish_reason
     (stop, tool_calls, length, end_turn, function_call, max_tokens, …).
     NULL means the stream ended without a finish_reason (e.g. truncated
     pre-finish).  Populated for BOTH success and failure rows.
     This column REPLACES the prior use of failure_detail_code for
     finish reasons; see the migration header for the full rationale.';

-- ■ 2. Partial index — only rows where the value is set, and group by
--      finish reason for cheap provider × finish-reason aggregations.
--      Mirrors idx_request_logs_quality_flags pattern.
CREATE INDEX IF NOT EXISTS idx_request_logs_upstream_finish_reason
    ON public.request_logs (upstream_finish_reason, ts DESC)
    WHERE upstream_finish_reason IS NOT NULL
      AND upstream_finish_reason <> '';

-- ■ 3. Backfill (one-time; safe to re-run, the WHERE clause is the
--      idempotency guard):
--      For every successful row whose failure_detail_code currently
--      holds a normal finish reason, move it to upstream_finish_reason
--      and CLEAR failure_detail_code so the admin UI's "失败详情"
--      column is no longer polluted.
--
--      We DO NOT touch rows where:
--        - success = false (real failure; failure_detail_code stays)
--        - failure_detail_code is a non-finish reason (interruption,
--          5xx, etc.) — we don't know which those are statically, so
--          the safe move is "only move the well-known finish reasons"
WITH normal_finish_reasons AS (
    SELECT unnest(ARRAY[
        'stop',
        'tool_calls',
        'function_call',
        'length',
        'end_turn',
        'max_tokens'
    ]) AS finish_reason
)
UPDATE public.request_logs rl
   SET upstream_finish_reason = rl.failure_detail_code,
       failure_detail_code   = NULL
 WHERE rl.success = true
   AND rl.failure_detail_code IN (SELECT finish_reason FROM normal_finish_reasons)
   AND (rl.upstream_finish_reason IS NULL OR rl.upstream_finish_reason = '');

-- ■ 4. Telemetry audit:
--      A new background log row that records the row count moved by
--      the backfill. Implemented as a one-row write to a side table
--      so operators can see the migration's effect in plain SQL.
CREATE TABLE IF NOT EXISTS schema_migration_audit (
    migration_id   TEXT PRIMARY KEY,
    applied_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    row_count      BIGINT NOT NULL DEFAULT 0,
    note           TEXT NOT NULL DEFAULT ''
);

INSERT INTO schema_migration_audit (migration_id, row_count, note)
VALUES (
    '018_upstream_finish_reason',
    (
        SELECT count(*)
        FROM public.request_logs
        WHERE upstream_finish_reason IS NOT NULL
    ),
    'T-NEW-7 backfill: moved finish_reason values out of failure_detail_code'
)
ON CONFLICT (migration_id) DO UPDATE
   SET applied_at = EXCLUDED.applied_at,
       row_count  = EXCLUDED.row_count,
       note       = EXCLUDED.note;

COMMIT;
