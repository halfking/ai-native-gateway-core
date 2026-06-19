-- 019_passive_probe_state.sql
-- 2026-06-20 v5: passive probe observation table + layer-5 integration.
--
-- This migration adds:
--   1. passive_probe_state — tracks consecutive errors from request_logs
--   2. ALTER TABLE model_probe_state ADD COLUMN IF NOT EXISTS (new fields for v5)
--   3. Index for fast reviewing state queries
--
-- See docs/llm-gateway-go/2026-06-20-probe-strategy-free-models-endpoint.md

-- ── 1. passive_probe_state ───────────────────────────────────────────────
-- Tracks accumulated error counts per (credential, model, error_kind) from
-- passive observation (Layer 5). Used by the PassiveProbeListener worker for
-- the "consecutive >= 3 OR error_rate >= 0.6" trigger logic.
CREATE TABLE IF NOT EXISTS passive_probe_state (
    credential_id       INTEGER NOT NULL,
    raw_model_name      TEXT NOT NULL,
    error_kind          TEXT NOT NULL,           -- model_not_found, quota_*, rate_limit_*, auth_*, upstream_down
    consecutive_count   INTEGER NOT NULL DEFAULT 0,
    total_recent_count  INTEGER NOT NULL DEFAULT 0,
    window_total_count  INTEGER NOT NULL DEFAULT 0,
    first_seen_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    in_reviewing        BOOLEAN NOT NULL DEFAULT FALSE,
    reviewing_until     TIMESTAMPTZ,
    final_marked_at     TIMESTAMPTZ,
    unavailable_reason  TEXT,
    last_response_body_preview TEXT,              -- max 200 bytes (token-filtered)
    PRIMARY KEY (credential_id, raw_model_name, error_kind)
);

CREATE INDEX IF NOT EXISTS idx_passive_probe_reviewing
    ON passive_probe_state (in_reviewing, reviewing_until)
    WHERE in_reviewing = TRUE;

COMMENT ON TABLE passive_probe_state IS
    'v5: Passive observation state for Layer 5. Accumulates consecutive errors from request_logs for the secondary-verification trigger (consecutive>=3 or error_rate>=0.6).';

-- ── 2. model_probe_state v5 columns ──────────────────────────────────────
-- Add fields for the expanded error classification (quota/rate_limit/etc.)
ALTER TABLE model_probe_state
    ADD COLUMN IF NOT EXISTS last_unavailable_reason TEXT,
    ADD COLUMN IF NOT EXISTS last_err_code TEXT,
    ADD COLUMN IF NOT EXISTS next_retry_at_override TIMESTAMPTZ;

-- ── 3. Index for fast reviewing / retry queries ──────────────────────────
CREATE INDEX IF NOT EXISTS idx_model_probe_state_retry
    ON model_probe_state (state, next_retry_at)
    WHERE state = 'recovering';
