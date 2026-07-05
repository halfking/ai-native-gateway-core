-- Migration 037: candidate_failure_logs table
-- Created: 2026-06-23
-- Purpose: Record every per-credential / per-model upstream failure so that
--   transient errors (e.g. the cred-11/minimax-m3 33.8% failure rate from
--   2026-06-23) become diagnosable. Previously the routing layer only logged
--   a single ExecuteError with the LAST candidate's error, losing the
--   other candidates' details. The Phase 1 P0 fix added upstream response
--   body to request_logs.response_preview, but operators still couldn't
--   see WHICH credentials were failing in a sequence.
--
-- Schema:
--   - One row per (request, credential, model, attempt) when an upstream
--     call to that binding fails. attempt_index matches the routing layer's
--     attempt counter so retries are visible.
--   - indexed on (credential_id, ts DESC) and (request_id) for fast
--     history queries by credential and full per-request trace.
--   - upstream_status_code and upstream_response_body are the vendor-side
--     diagnostics. Body is capped at 1KB to bound storage.
--
-- Idempotent: uses IF NOT EXISTS so re-running is a no-op.

CREATE TABLE IF NOT EXISTS candidate_failure_logs (
    id                         BIGSERIAL PRIMARY KEY,
    request_id                 TEXT NOT NULL,
    ts                         TIMESTAMPTZ NOT NULL DEFAULT now(),
    tenant_id                  TEXT NOT NULL DEFAULT 'default',
    credential_id              INT NOT NULL,
    provider_id                INT NOT NULL,
    raw_model_name             TEXT NOT NULL,
    attempt_index              INT NOT NULL DEFAULT 0,
    error_kind                 TEXT NOT NULL,
    error_message              TEXT,
    upstream_status_code       INT,
    upstream_response_body     TEXT,
    upstream_response_preview  TEXT,
    latency_ms                 INT,
    retryable                  BOOLEAN,
    context                    JSONB
);

CREATE INDEX IF NOT EXISTS idx_candidate_failure_logs_cred_ts
    ON candidate_failure_logs (credential_id, ts DESC);

CREATE INDEX IF NOT EXISTS idx_candidate_failure_logs_provider_ts
    ON candidate_failure_logs (provider_id, ts DESC);

CREATE INDEX IF NOT EXISTS idx_candidate_failure_logs_req
    ON candidate_failure_logs (request_id);

CREATE INDEX IF NOT EXISTS idx_candidate_failure_logs_model_ts
    ON candidate_failure_logs (raw_model_name, ts DESC);

COMMENT ON TABLE candidate_failure_logs IS
    'Per-credential, per-model upstream failure log. Populated by routing/executor.go on every failed candidate attempt so transient diagnostics surface the actual vendor response (kind, status, body) instead of a generic "all N candidates failed" message. Used by candidate_failure_monitor for alerts and the admin candidate-failure API.';
