-- 017: Quality fix mode + request_log quality signal columns.
--
-- Motivation (2026-06-19):
--   Upstream providers sometimes return malformed tool_call payloads:
--     - empty `function.name` (gpt-5.4, kaixuan-routed via api.vapeur.ai)
--     - duplicate `tool_call.id` within one message
--     - tool_call array with embedded XML text (minimax M2.7 fallback)
--   These trigger `Model tried to call unavailable tool ''` in AI SDK
--   consumers and break the agent loop.  We add a per-provider switch so
--   the gateway can either:
--     off         — passthrough (current behavior; preserve raw upstream)
--     detect_only — detect & write request_log signals but do NOT modify
--     fix         — detect + write signals + actively rewrite the body
--                   (rename empty name to `__unknown_tool_<i>__`, dedup ids)
--   Operators can then SQL-trace which providers are "watering down"
--   quality by aggregating quality_flags + quality_score.
--
-- This migration is idempotent and additive — no destructive change to
-- existing rows.  Existing request_logs rows get quality_flags='{}',
-- quality_fix_actions='{}'::jsonb, quality_score=NULL.

BEGIN;

-- 1. providers.quality_fix_mode (default off — does not change behavior)
ALTER TABLE providers
    ADD COLUMN IF NOT EXISTS quality_fix_mode TEXT NOT NULL DEFAULT 'off'
        CHECK (quality_fix_mode IN ('off', 'detect_only', 'fix'));

COMMENT ON COLUMN providers.quality_fix_mode IS
    'off         : passthrough, no detection, no rewrite.
     detect_only : detect tool_call quality issues, write request_log signals,
                   but do NOT modify the response body sent to the client.
     fix         : detect + write signals + rewrite the response body
                   (rename empty names, dedup ids, etc.) before forwarding.';

-- 2. request_logs.quality_flags (TEXT[]) — detected problem tags
--    Values include: empty_tool_name, duplicate_tool_call_id,
--                    xml_in_tool_calls, truncated_stream, etc.
ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS quality_flags TEXT[] NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS idx_request_logs_quality_flags
    ON request_logs USING GIN (quality_flags)
    WHERE cardinality(quality_flags) > 0;

-- 3. request_logs.quality_fix_actions (JSONB) — what was actually done
--    Shape: {"empty_tool_name": {"detected": 2, "renamed": 1, "dropped": 1},
--            "duplicate_tool_call_id": {"detected": 1, "renamed": 1}, ...}
ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS quality_fix_actions JSONB NOT NULL DEFAULT '{}'::jsonb;

-- 4. request_logs.quality_score (NUMERIC(3,2)) — overall quality 0..1
--    NULL means "not evaluated" (legacy rows + off-mode rows).
ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS quality_score NUMERIC(3,2)
        CHECK (quality_score IS NULL OR (quality_score >= 0 AND quality_score <= 1));

CREATE INDEX IF NOT EXISTS idx_request_logs_provider_quality
    ON request_logs (provider_id, quality_score, ts DESC)
    WHERE quality_score IS NOT NULL;

-- 5. provider_quality_rollup (5-min bucket per provider) — for dashboards
CREATE TABLE IF NOT EXISTS provider_quality_rollup (
    provider_id       INT  NOT NULL,
    bucket_start      TIMESTAMPTZ NOT NULL,
    total_requests    INT  NOT NULL DEFAULT 0,
    bad_requests      INT  NOT NULL DEFAULT 0,
    fixed_requests    INT  NOT NULL DEFAULT 0,
    avg_quality_score NUMERIC(3,2),
    top_flag          TEXT,
    PRIMARY KEY (provider_id, bucket_start)
);

CREATE INDEX IF NOT EXISTS idx_provider_quality_rollup_bucket
    ON provider_quality_rollup (bucket_start DESC);

COMMIT;
