-- 010_model_probe_runs.sql
-- Per-model probe test records. Each row is one (credential × model × attempt).
-- Used by:
--   1. bg/model_probe.go: writes a row after every probe attempt.
--   2. admin/handlers_probe.go: GET /api/providers/:id/probe-history reads this
--      to render the "查看自动测试记录" panel on the providers page.
--   3. admin/handlers_discovery.go: failed-count summary badge in model
--      discovery column.
--
-- Spec: 2026-06-18-model-probe-rounds

CREATE TABLE IF NOT EXISTS model_probe_runs (
    id                BIGSERIAL PRIMARY KEY,
    tenant_id         TEXT        NOT NULL DEFAULT 'default',
    credential_id     BIGINT      NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    raw_model_name    TEXT        NOT NULL,
    -- 'ok'        — request returned 2xx
    -- 'http_4xx'  — upstream returned 4xx (model not found / quota / etc.)
    -- 'http_5xx'  — upstream returned 5xx
    -- 'network'   — connect / TLS / timeout error
    -- 'auth'      — 401 / 403 (decrypt or upstream auth failed)
    -- 'skipped'   — manual_disabled / suspended / etc.
    status            TEXT        NOT NULL,
    http_status       INTEGER,
    error_code        TEXT,
    error_message     TEXT,
    latency_ms        INTEGER     NOT NULL DEFAULT 0,
    -- Did this run flip availability? 'recovered' | 'broke' | 'unchanged'
    state_change      TEXT,
    -- Was the action applied? false when skipped because of manual_disabled.
    state_applied     BOOLEAN     NOT NULL DEFAULT TRUE,
    triggered_by      TEXT        NOT NULL DEFAULT 'scheduler',  -- 'scheduler' | 'manual'
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_mpr_cred_created
    ON model_probe_runs (credential_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_mpr_model_created
    ON model_probe_runs (raw_model_name, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_mpr_status_created
    ON model_probe_runs (status, created_at DESC)
    WHERE status <> 'ok';

-- Quick "recent failed count per model" view for the model discovery
-- failed-count badge.  A run is "recent" if it was attempted in the last
-- 6 hours AND ended non-ok AND was not skipped (i.e. a real failure).
CREATE OR REPLACE VIEW v_recent_model_probe_failures AS
SELECT raw_model_name,
       credential_id,
       COUNT(*) AS failed_count,
       MAX(created_at) AS last_failed_at,
       MIN(error_code) AS sample_error_code
FROM model_probe_runs
WHERE status <> 'ok'
  AND status <> 'skipped'
  AND created_at > NOW() - INTERVAL '6 hours'
GROUP BY raw_model_name, credential_id;

COMMENT ON TABLE model_probe_runs IS
    'Per-(credential, model) probe attempts. Drives the providers-page "auto-test" panel and the model-discovery failed-count badge.';
COMMENT ON VIEW v_recent_model_probe_failures IS
    'Last 6h failed probe count, grouped by (model, credential). Used by model discovery UI badge.';
