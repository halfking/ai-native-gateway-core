-- 389_route_incidents.sql
-- Phase 1 read-only route incident diagnosis (spec
-- docs/superpowers/specs/2026-07-13-route-incident-diagnosis-design.md).
--
-- Two tables:
--   1. route_incidents          — current aggregate (one row per active or
--                                 recovering route incident; recovered rows
--                                 are kept for timeline/audit).
--   2. route_incident_events    — immutable, append-only evidence trail.
--
-- Design notes (consistent with the spec):
--   * Route identity = tenant_id + endpoint_protocol + canonical_or_outbound
--     model + provider_id + credential_id. tenant_id is part of the unique
--     key from day one so the later tenant-facing release does not need an
--     identity migration.
--   * Only one active or recovering incident per route (partial unique
--     index). A recovered row can be kept; the next failure on the same
--     route inserts a new row.
--   * State transitions are owned by domains/routeincident/store.go using
--     SELECT ... FOR UPDATE on the active aggregate row, and idempotent
--     event insertion keyed by (incident_id, request_id, terminal_status).
--   * tenant_id is NOT NULL — cross-tenant resources return 404, never
--     expose existence. provider_id / credential_id are nullable because
--     the upstream may not have been resolved (e.g. credential pool empty).

BEGIN;

-- ───────────────────────────────────────────────────────────────
-- route_incidents — current aggregate
-- ───────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS route_incidents (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Route identity (all five pieces are required for the row to be
    -- a real route; provider_id / credential_id may be NULL only when
    -- the request never reached a resolved candidate).
    tenant_id           TEXT NOT NULL,
    endpoint_protocol   TEXT NOT NULL,                -- e.g. "openai_chat_completions"
    model               TEXT NOT NULL,                -- canonical_name when present, else outbound_model
    provider_id         BIGINT,                       -- providers.id (NULL = unresolved)
    credential_id       BIGINT,                       -- credentials.id (NULL = unresolved)

    -- Lifecycle
    state               TEXT NOT NULL
        CHECK (state IN ('active', 'recovering', 'recovered')),
    failure_streak      INT  NOT NULL DEFAULT 0,
    recovery_streak     INT  NOT NULL DEFAULT 0,

    -- Aggregate metrics (incremented by the observer in lock-step with
    -- the state transition; kept here so the dashboard can render a
    -- detail page without re-aggregating request_logs).
    first_failure_at    TIMESTAMPTZ NOT NULL,
    last_failure_at     TIMESTAMPTZ,
    last_success_at     TIMESTAMPTZ,
    recovered_at        TIMESTAMPTZ,
    total_failures      BIGINT NOT NULL DEFAULT 0,
    total_successes     BIGINT NOT NULL DEFAULT 0,
    last_error_kind     TEXT,
    last_failure_stage  TEXT,                         -- "gateway" | "upstream" | NULL

    -- Resolution / operator metadata (Phase 2 writes here; Phase 1 leaves NULL)
    resolution_source   TEXT,                         -- e.g. "reprobe", "recover"
    resolved_by_user    TEXT,
    resolved_reason     TEXT,

    -- Optimistic concurrency
    version             BIGINT NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One active/recovering incident per route key. Recovered rows are
-- excluded so a recovered row can be kept on disk for audit/timeline
-- queries without blocking the next failure on the same route from
-- creating a fresh incident.
CREATE UNIQUE INDEX IF NOT EXISTS uq_route_incidents_active_route
    ON route_incidents (
        tenant_id, endpoint_protocol, model, COALESCE(provider_id, 0), COALESCE(credential_id, 0)
    )
    WHERE state IN ('active', 'recovering');

CREATE INDEX IF NOT EXISTS idx_route_incidents_state_updated
    ON route_incidents (state, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_route_incidents_tenant_state
    ON route_incidents (tenant_id, state, updated_at DESC);

-- Updated_at maintenance.
CREATE OR REPLACE FUNCTION touch_route_incidents_updated_at()
RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS route_incidents_touch ON route_incidents;
CREATE TRIGGER route_incidents_touch
    BEFORE UPDATE ON route_incidents
    FOR EACH ROW EXECUTE FUNCTION touch_route_incidents_updated_at();


-- ───────────────────────────────────────────────────────────────
-- route_incident_events — append-only evidence trail
-- ───────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS route_incident_events (
    id                  BIGSERIAL PRIMARY KEY,
    incident_id         UUID NOT NULL REFERENCES route_incidents(id) ON DELETE CASCADE,

    -- event_type covers: opened | failure_observed | recovery_progress |
    -- recovered | diagnostic_run | operator_action.
    event_type          TEXT NOT NULL
        CHECK (event_type IN (
            'opened', 'failure_observed', 'recovery_progress',
            'recovered', 'diagnostic_run', 'operator_action'
        )),

    -- The persisted request_log this event ties to. request_id is the
    -- server-generated UUID; the (incident_id, request_id, terminal_status)
    -- triple is the idempotency key for the observer.
    request_id          TEXT,
    terminal_status     TEXT,                         -- 'success' | 'failure' | NULL for non-request events
    failure_kind        TEXT,
    failure_stage       TEXT,                         -- 'gateway' | 'upstream' | NULL
    failure_streak      INT,
    recovery_streak     INT,

    -- Sanitized structured evidence (NEVER includes credentials, full
    -- request/response bodies, headers, or raw upstream errors).
    -- Phase 1 stores only counts/kinds; Phase 2 may add diagnostic_run_id.
    evidence            JSONB NOT NULL DEFAULT '{}'::jsonb,

    actor               TEXT,                         -- 'system' | super-admin user id
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Idempotency: the observer can retry the same (request_id, terminal_status)
-- safely. NULL request_id rows (system events) are still allowed because
-- terminal_status is also NULL for them.
CREATE UNIQUE INDEX IF NOT EXISTS uq_route_incident_events_idem
    ON route_incident_events (incident_id, request_id, terminal_status)
    WHERE request_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_route_incident_events_incident_created
    ON route_incident_events (incident_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_route_incident_events_type_created
    ON route_incident_events (event_type, created_at DESC);


-- ───────────────────────────────────────────────────────────────
-- helper: get_current_tenant() already exists in the public schema; the
-- store will filter every read by tenant_id explicitly so we do not rely
-- on RLS for cross-tenant prevention. RLS is left off for now (Phase 1
-- is super-admin only).
-- ───────────────────────────────────────────────────────────────

COMMENT ON TABLE route_incidents IS
    'Phase-1 read-only route incident aggregate. One active/recovering row per route key (tenant + protocol + model + provider + credential). Recovered rows are retained for timeline/audit. Cross-tenant reads return 404 at the API layer.';
COMMENT ON TABLE route_incident_events IS
    'Immutable, append-only evidence trail for route_incidents. Each (incident_id, request_id, terminal_status) triple is unique so the observer can retry safely.';

COMMIT;
