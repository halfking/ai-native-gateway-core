-- ===========================================================================
-- File:          sql/migrations/startup/454_response_format_anomalies.sql
-- Database:      llm_gateway
-- Object Type:   TABLE
-- Object Name:   response_format_anomalies
-- Purpose:       Track format & data anomalies (extraction failures,
--                NaN/Inf persistence failures, WAL marshal failures, etc.).
--                Bridges to fault_events via AnomalyHarvester.
--
-- Mirrors:       db/db.go:ensureResponseFormatAnomaliesSchema()
-- Status:        active
-- Idempotent:    YES (CREATE IF NOT EXISTS / DROP+CREATE POLICY)
-- Dependencies:  fault_events (migration 375), public.get_current_tenant()
--
-- Changelog:
--   2026-07-23  v1.0  Initial creation (mirrored from Go startup ensure*)
-- ===========================================================================
--
-- Execution:
--   psql -h $DB_HOST -U $DB_USER -d llm_gateway -f 454_response_format_anomalies.sql
--
-- Verification:
--   \dt response_format_anomalies
--   \d response_format_anomalies
--
-- Rollback:
--   See 454_response_format_anomalies.down.sql
-- ===========================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS response_format_anomalies (
    id                  BIGSERIAL PRIMARY KEY,
    detected_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    request_id          TEXT NOT NULL,
    provider_id         INT,
    provider_code       TEXT,
    client_model        TEXT,
    outbound_model      TEXT,
    anomaly_type        TEXT NOT NULL,
    severity            TEXT NOT NULL DEFAULT 'medium',
    usage_source        TEXT,
    expected_tokens     INT,
    actual_tokens       INT,
    content_size_bytes  INT,
    response_structure  JSONB,
    response_sample     TEXT,
    resolved            BOOLEAN NOT NULL DEFAULT false,
    resolved_at         TIMESTAMPTZ,
    resolution_notes    TEXT,
    tenant_id           TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_response_format_anomalies_detected_at
    ON response_format_anomalies(detected_at DESC);

CREATE INDEX IF NOT EXISTS idx_response_format_anomalies_request_id
    ON response_format_anomalies(request_id);

CREATE INDEX IF NOT EXISTS idx_response_format_anomalies_provider
    ON response_format_anomalies(provider_code, client_model)
    WHERE provider_code IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_response_format_anomalies_type
    ON response_format_anomalies(anomaly_type, detected_at DESC);

-- Optimized for AnomalyHarvester bridge query: WHERE detected_at > $1
-- AND resolved = FALSE GROUP BY (anomaly_type, severity). Composite index
-- avoids sequential scan when anomaly count grows.
CREATE INDEX IF NOT EXISTS idx_response_format_anomalies_bridge
    ON response_format_anomalies(resolved, detected_at, anomaly_type, severity)
    WHERE NOT resolved;

CREATE INDEX IF NOT EXISTS idx_response_format_anomalies_unresolved
    ON response_format_anomalies(detected_at DESC)
    WHERE NOT resolved;

ALTER TABLE response_format_anomalies ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS response_format_anomalies_tenant_isolation ON public.response_format_anomalies;
CREATE POLICY response_format_anomalies_tenant_isolation ON public.response_format_anomalies
    USING (tenant_id IS NULL OR tenant_id = public.get_current_tenant());

DROP POLICY IF EXISTS response_format_anomalies_super_admin ON public.response_format_anomalies;
CREATE POLICY response_format_anomalies_super_admin ON public.response_format_anomalies
    USING (current_setting('app.bypass_rls', true) = 'true');

CREATE OR REPLACE VIEW v_format_anomaly_summary AS
SELECT
    DATE_TRUNC('hour', detected_at) AS hour,
    provider_code,
    client_model,
    anomaly_type,
    severity,
    COUNT(*) AS anomaly_count,
    COUNT(DISTINCT request_id) AS affected_requests,
    AVG(content_size_bytes) AS avg_content_size,
    AVG(expected_tokens) AS avg_expected_tokens,
    AVG(actual_tokens) AS avg_actual_tokens,
    COUNT(*) FILTER (WHERE resolved) AS resolved_count
FROM response_format_anomalies
WHERE detected_at > NOW() - INTERVAL '7 days'
GROUP BY 1, 2, 3, 4, 5;

COMMIT;