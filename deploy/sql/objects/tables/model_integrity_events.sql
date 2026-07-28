-- ===========================================================================
-- File:          deploy/sql/objects/tables/model_integrity_events.sql
-- Database:      llm_gateway
-- Object Type:   TABLE
-- Object Name:   model_integrity_events
-- Purpose:       2026-07-28: Operational mirror of startup migration 462.
--                Used by deploy scripts that bootstrap a fresh DB from
--                /opt/llm-gateway-go/sql/objects/ (instead of replaying
--                startup migrations in order).
--
-- Mirrors:       sql/migrations/startup/462_model_integrity_events.sql
-- Idempotent:    YES
-- Changelog:
--   2026-07-28  v1.0  Initial creation
-- ===========================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS model_integrity_events (
    id                  BIGSERIAL PRIMARY KEY,
    ts                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    request_id          TEXT,
    tenant_id           TEXT,
    application_id      INT,
    api_key_id          INT,
    provider_id         INT,
    provider_code       TEXT,
    credential_id       INT,
    client_model        TEXT,
    outbound_model      TEXT,
    raw_model_name      TEXT,
    anomaly_type        TEXT NOT NULL,
    severity            TEXT NOT NULL DEFAULT 'low',
    expected_value      TEXT,
    actual_value        TEXT,
    sample              TEXT,
    context             JSONB,
    resolved            BOOLEAN NOT NULL DEFAULT false,
    resolved_at         TIMESTAMPTZ,
    resolution_notes    TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_model_integrity_events_ts
    ON model_integrity_events(ts DESC);

CREATE INDEX IF NOT EXISTS idx_model_integrity_events_cred_model_type
    ON model_integrity_events(credential_id, raw_model_name, anomaly_type, ts DESC);

CREATE INDEX IF NOT EXISTS idx_model_integrity_events_provider_type
    ON model_integrity_events(provider_id, anomaly_type, ts DESC);

CREATE INDEX IF NOT EXISTS idx_model_integrity_events_request_id
    ON model_integrity_events(request_id)
    WHERE request_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_model_integrity_events_bridge
    ON model_integrity_events(resolved, ts, anomaly_type, severity)
    WHERE resolved = FALSE;

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
