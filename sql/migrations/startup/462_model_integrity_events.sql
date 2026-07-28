-- ===========================================================================
-- File:          sql/migrations/startup/462_model_integrity_events.sql
-- Database:      llm_gateway
-- Object Type:   TABLE
-- Object Name:   model_integrity_events
-- Purpose:       2026-07-28: Per-request and per-(cred,model) integrity events.
--                Detects: model identity mismatch (silent substitution), finish
--                refusal/truncation, token-arithmetic failure, empty responses,
--                repeated content, and 7-day system_fingerprint drift.
--
--                Independent from response_format_anomalies (which tracks
--                format/parse anomalies only). Admin UI surfaces both via
--                separate endpoints; AnomalyHarvester can bridge either.
--
-- Mirrors:       db/db.go:ensureModelIntegrityEventsSchema()
-- Status:        active
-- Idempotent:    YES (CREATE TABLE IF NOT EXISTS / DROP+CREATE POLICY)
-- Dependencies:  request_logs (request_id join), public.get_current_tenant()
-- Changelog:
--   2026-07-28  v1.0  Initial creation (model-integrity-20260728)
-- ===========================================================================
--
-- Execution:
--   psql -h $DB_HOST -U $DB_USER -d llm_gateway -f 462_model_integrity_events.sql
--
-- Verification:
--   \dt model_integrity_events
--   \d model_integrity_events
--
-- Rollback:
--   See 462_model_integrity_events.down.sql
-- ===========================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS model_integrity_events (
    id                  BIGSERIAL PRIMARY KEY,
    ts                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    request_id          TEXT,                    -- nullable: fingerprint drift has no per-request context
    tenant_id           TEXT,
    application_id      INT,
    api_key_id          INT,
    provider_id         INT,
    provider_code       TEXT,
    credential_id       INT,
    client_model        TEXT,
    outbound_model      TEXT,
    raw_model_name      TEXT,
    -- model_mismatch | finish_refusal | finish_truncation
    -- | token_arith_fail | empty_response | repeated_content | fingerprint_drift
    anomaly_type        TEXT NOT NULL,
    severity            TEXT NOT NULL DEFAULT 'low',  -- low | medium | high | critical
    expected_value      TEXT,
    actual_value        TEXT,
    -- PII-safe sample: provider_response_id, system_fingerprint, finish_reason,
    -- usage_source, chunk_count. NEVER contains user prompt or model output.
    sample              TEXT,
    context             JSONB,                       -- structured signal context
    resolved            BOOLEAN NOT NULL DEFAULT false,
    resolved_at         TIMESTAMPTZ,
    resolution_notes    TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Time-ordered scan: most-recent first (admin dashboard / "recent" endpoint).
CREATE INDEX IF NOT EXISTS idx_model_integrity_events_ts
    ON model_integrity_events(ts DESC);

-- Per-(credential, model, anomaly) drill-down: "did c16/glm-5.2 drift today?"
CREATE INDEX IF NOT EXISTS idx_model_integrity_events_cred_model_type
    ON model_integrity_events(credential_id, raw_model_name, anomaly_type, ts DESC);

-- Per-provider rollup: "is provider X getting a spike in mismatches?"
CREATE INDEX IF NOT EXISTS idx_model_integrity_events_provider_type
    ON model_integrity_events(provider_id, anomaly_type, ts DESC);

-- Join with request_logs by request_id for per-request timeline.
CREATE INDEX IF NOT EXISTS idx_model_integrity_events_request_id
    ON model_integrity_events(request_id)
    WHERE request_id IS NOT NULL;

-- Bridge to AnomalyHarvester fault_events (uses same partial index pattern as
-- response_format_anomalies_unresolved).
CREATE INDEX IF NOT EXISTS idx_model_integrity_events_bridge
    ON model_integrity_events(resolved, ts, anomaly_type, severity)
    WHERE resolved = FALSE;

-- RLS: tenant isolation (mirrors response_format_anomalies policy).
ALTER TABLE model_integrity_events ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS model_integrity_events_tenant_isolation ON public.model_integrity_events;
CREATE POLICY model_integrity_events_tenant_isolation ON public.model_integrity_events
    USING (
        tenant_id IS NULL
        OR tenant_id::text = COALESCE(
            current_setting('app.current_tenant', true),
            public.get_current_tenant()
        )
    );

DROP POLICY IF EXISTS model_integrity_events_super_admin ON public.model_integrity_events;
CREATE POLICY model_integrity_events_super_admin ON public.model_integrity_events
    FOR ALL
    USING (current_setting('app.bypass_rls', true) = 'true');

COMMIT;
