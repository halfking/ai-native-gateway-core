-- 390_routing_audit_log.sql
-- Phase 2 of the route-incident diagnosis feature. Adds:
--   1. routing_audit_log   — immutable, append-only audit trail for
--      every mutating action and diagnostic export. The row is
--      committed in the same transaction as the action state
--      transition (see spec §"Phase Two Diagnostic Runs And Actions").
--   2. diagnostic_runs     — persisted, sanitized result of a
--      diagnostic test (direct_upstream_test, through_gateway_test,
--      reprobe, release_slot, reset_slots, reset_availability,
--      recover). The row is also immutable once finalized; the
--      action handler updates a single `state` column.
--
-- Both tables are tenant-scoped on tenant_id and accept no
-- credentials, headers, full request/response bodies, or raw
-- upstream errors (the DTO is sanitized at the application layer
-- before INSERT).

BEGIN;

-- ───────────────────────────────────────────────────────────────
-- routing_audit_log — append-only audit trail
-- ───────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS routing_audit_log (
    id                  BIGSERIAL PRIMARY KEY,
    incident_id         UUID REFERENCES route_incidents(id) ON DELETE SET NULL,
    tenant_id           TEXT NOT NULL,

    -- Action classification. The allowed set is the same allowlist
    -- enforced by the application layer; this CHECK is a defensive
    -- backstop so a future bug cannot write an unexpected action.
    action              TEXT NOT NULL
        CHECK (action IN (
            'direct_upstream_test', 'through_gateway_test',
            'reprobe', 'release_slot', 'reset_slots',
            'reset_availability', 'recover',
            'evidence_export'
        )),

    -- Authenticated actor. We never store raw user agents,
    -- session cookies, or auth headers — only the user id.
    actor               TEXT NOT NULL,
    reason              TEXT NOT NULL,                  -- operator-provided, length-bounded in app layer

    -- Confirmation token hash (SHA-256 of the operator-supplied
    -- short-lived token). The plaintext is never stored.
    confirmation_token_hash TEXT NOT NULL,

    -- Idempotency key (the operator can retry safely; the unique
    -- index rejects duplicate executions).
    idempotency_key     TEXT NOT NULL,

    -- Sanitized structured evidence. Allow-list enforced in app
    -- layer; this column is JSONB for forward compatibility.
    request_payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    pre_snapshot        JSONB NOT NULL DEFAULT '{}'::jsonb,  -- state BEFORE the action
    post_snapshot       JSONB NOT NULL DEFAULT '{}'::jsonb,  -- state AFTER (or partial on failure)
    response_payload    JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- Outcome — the action may legitimately fail (e.g. version
    -- mismatch, route no longer in active state). We record the
    -- outcome explicitly so an audit query can distinguish
    -- "operator took a noop because route already recovered" from
    -- "operator never tried".
    outcome             TEXT NOT NULL
        CHECK (outcome IN ('success', 'noop', 'failed')),
    failure_reason      TEXT,

    -- Diagnostic run id (for actions that produced a run).
    diagnostic_run_id   UUID,

    -- IP is recorded ONLY as a short hash, never the raw value,
    -- to keep cross-tenant auditing possible without leaking the
    -- operator's network identity to the row.
    actor_ip_hash       TEXT,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Idempotency: the SAME (idempotency_key) MUST NOT execute twice.
-- The action handler treats 23505 unique_violation on this index
-- as "already executed; return the cached result". This is the
-- canonical mechanism for "operator clicked the button twice".
CREATE UNIQUE INDEX IF NOT EXISTS uq_routing_audit_log_idem
    ON routing_audit_log (idempotency_key);

CREATE INDEX IF NOT EXISTS idx_routing_audit_log_incident_created
    ON routing_audit_log (incident_id, created_at DESC)
    WHERE incident_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_routing_audit_log_tenant_created
    ON routing_audit_log (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_routing_audit_log_actor_created
    ON routing_audit_log (actor, created_at DESC);


-- ───────────────────────────────────────────────────────────────
-- diagnostic_runs — persisted test results
-- ───────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS diagnostic_runs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id         UUID NOT NULL REFERENCES route_incidents(id) ON DELETE CASCADE,
    tenant_id           TEXT NOT NULL,

    -- Test kind. Matches the action allowlist (subset).
    kind                TEXT NOT NULL
        CHECK (kind IN (
            'direct_upstream_test', 'through_gateway_test',
            'reprobe', 'release_slot', 'reset_slots',
            'reset_availability', 'recover'
        )),

    -- Lifecycle of the run itself: pending → running → succeeded |
    -- failed. Final states are immutable.
    state               TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),

    -- Sanitized inputs. We do NOT store the request body, the
    -- upstream URL (re-derived from provider config), or any
    -- operator-supplied raw text.
    route_key           JSONB NOT NULL,                  -- tenant + protocol + model + provider + credential
    parameters          JSONB NOT NULL DEFAULT '{}'::jsonb,  -- action-specific, allow-listed keys only
    started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at         TIMESTAMPTZ,

    -- Sanitized result. Status code (int), latency (ms),
    -- classification label, error_kind (sanitized), step-by-step
    -- step[] of {stage, status, latency_ms}. NEVER request body,
    -- NEVER response body, NEVER auth header.
    result              JSONB NOT NULL DEFAULT '{}'::jsonb,

    -- Reference to the audit row that produced this run.
    audit_log_id        BIGINT REFERENCES routing_audit_log(id) ON DELETE SET NULL,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_incident
    ON diagnostic_runs (incident_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_tenant
    ON diagnostic_runs (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_state
    ON diagnostic_runs (state, started_at DESC);

-- The "recover" / "reprobe" runs are exactly one-per-incident in
-- the spec's intended model; we still allow multiple (a recovered
-- route can re-fail), but the UI's evidence export must pick the
-- most recent succeeded run.
CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_kind_state
    ON diagnostic_runs (kind, state, started_at DESC);

-- Reuse the existing updated_at trigger function from
-- route_incidents (idempotent).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'touch_route_incidents_updated_at'
    ) THEN
        CREATE FUNCTION touch_route_incidents_updated_at()
        RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN
            NEW.updated_at := now();
            RETURN NEW;
        END;
        $$;
    END IF;
END $$;

DROP TRIGGER IF EXISTS diagnostic_runs_touch ON diagnostic_runs;
CREATE TRIGGER diagnostic_runs_touch
    BEFORE UPDATE ON diagnostic_runs
    FOR EACH ROW EXECUTE FUNCTION touch_route_incidents_updated_at();

COMMENT ON TABLE routing_audit_log IS
    'Phase-2 append-only audit trail. Every mutating action and evidence export is recorded with the authenticated actor, the confirmation-token hash, an idempotency key, and a before/after snapshot. Rows are immutable once committed; the unique index on idempotency_key guarantees that operator retries do not double-execute.';
COMMENT ON TABLE diagnostic_runs IS
    'Phase-2 sanitized results of a single diagnostic test. request body, response body, auth headers, raw upstream errors, and the upstream URL are NEVER stored. The route key is recorded so the result can be associated with a specific incident.';

COMMIT;
