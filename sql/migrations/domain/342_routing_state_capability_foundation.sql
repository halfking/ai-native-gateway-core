-- Migration 342: routing state compatibility and capability profile foundation
--
-- Keeps the runtime state schema aligned with its constraints, restores the
-- live recent-success-rate source, and introduces canonical capability profile
-- storage for shadow-only substitution analysis.
--
-- Safe to re-run. This migration intentionally does not change routing or
-- probe behaviour.

BEGIN;

-- Historical deployments briefly accepted availability_state='degraded' even
-- though the current constraint has no such value. Preserve its recoverable
-- semantics as cooling and retain a queryable migration reason.
UPDATE credentials
SET availability_state = 'cooling',
    state_reason_code = COALESCE(NULLIF(state_reason_code, ''), 'legacy_degraded'),
    state_updated_at = now()
WHERE availability_state = 'degraded';

UPDATE credentials
SET health_status = 'unreachable',
    health_error = COALESCE(NULLIF(health_error, ''), 'legacy_invalid_health_status'),
    health_checked_at = COALESCE(health_checked_at, now()),
    state_updated_at = now()
WHERE health_status IN ('error', 'auth_failed');

-- The runtime schema may predate the current constraint. Reapply the canonical
-- legal set after normalising legacy data.
ALTER TABLE credentials
    DROP CONSTRAINT IF EXISTS credentials_availability_state_check;
ALTER TABLE credentials
    ADD CONSTRAINT credentials_availability_state_check CHECK (
        availability_state IN ('ready', 'cooling', 'rate_limited', 'auth_failed', 'unreachable', 'suspended')
    );

ALTER TABLE credentials
    DROP CONSTRAINT IF EXISTS chk_credentials_health_status;
ALTER TABLE credentials
    ADD CONSTRAINT chk_credentials_health_status CHECK (
        health_status IN ('unknown', 'healthy', 'warning', 'unreachable')
    );

-- Live request rows remain in request_logs_hot for the full success-rate
-- window. Keep the database object and startup implementation consistent.
CREATE OR REPLACE FUNCTION recent_success_rate(
    p_credential_id BIGINT,
    p_raw_model     TEXT,
    p_sample_n      INT DEFAULT 50,
    p_window_hours  INT DEFAULT 3
)
RETURNS TABLE(rate DOUBLE PRECISION, samples INT)
LANGUAGE sql
STABLE
AS $$
    WITH recent AS (
        SELECT success
        FROM request_logs_hot
        WHERE credential_id = p_credential_id
          AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
          AND ts > NOW() - (p_window_hours || ' hours')::interval
        ORDER BY ts DESC
        LIMIT p_sample_n
    )
    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
           COUNT(*)::int
    FROM recent;
$$;

COMMENT ON FUNCTION recent_success_rate(bigint, text, int, int) IS
    'Success fraction over the most recent p_sample_n request_logs_hot rows within p_window_hours for a (credential, raw model) pair.';

CREATE INDEX IF NOT EXISTS idx_request_logs_hot_credential_model_ts
    ON request_logs_hot (
        credential_id,
        lower(COALESCE(outbound_model, client_model)),
        ts DESC
    );

-- Canonical, versioned capability data is deliberately separate from
-- models_canonical: the latter stores source facts, while this table stores
-- calculated/overridden routing policy inputs.
CREATE TABLE IF NOT EXISTS model_capability_profiles (
    canonical_id BIGINT PRIMARY KEY REFERENCES models_canonical(id) ON DELETE CASCADE,
    mode TEXT NOT NULL DEFAULT 'auto',
    modality_caps TEXT[] NOT NULL DEFAULT '{}',
    intelligence_level SMALLINT,
    context_level SMALLINT,
    response_speed_level SMALLINT,
    price_level SMALLINT,
    capability_score NUMERIC(5,2),
    manual_overrides JSONB NOT NULL DEFAULT '{}'::jsonb,
    computed_inputs JSONB NOT NULL DEFAULT '{}'::jsonb,
    calculation_version TEXT NOT NULL DEFAULT 'v1',
    updated_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT model_capability_profiles_mode_check CHECK (mode IN ('auto', 'manual', 'hybrid')),
    CONSTRAINT model_capability_profiles_intelligence_check CHECK (intelligence_level IS NULL OR intelligence_level BETWEEN 1 AND 10),
    CONSTRAINT model_capability_profiles_context_check CHECK (context_level IS NULL OR context_level BETWEEN 1 AND 10),
    CONSTRAINT model_capability_profiles_speed_check CHECK (response_speed_level IS NULL OR response_speed_level BETWEEN 1 AND 10),
    CONSTRAINT model_capability_profiles_price_check CHECK (price_level IS NULL OR price_level BETWEEN 1 AND 10),
    CONSTRAINT model_capability_profiles_score_check CHECK (capability_score IS NULL OR capability_score BETWEEN 0 AND 100)
);

CREATE INDEX IF NOT EXISTS idx_model_capability_profiles_score
    ON model_capability_profiles (capability_score DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS idx_model_capability_profiles_modality_caps
    ON model_capability_profiles USING GIN (modality_caps);

CREATE TABLE IF NOT EXISTS model_capability_profile_audit (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    canonical_id BIGINT NOT NULL REFERENCES models_canonical(id) ON DELETE CASCADE,
    action TEXT NOT NULL,
    actor TEXT,
    reason TEXT,
    profile_snapshot JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT model_capability_profile_audit_action_check CHECK (action IN ('create', 'update', 'recompute', 'delete'))
);

CREATE INDEX IF NOT EXISTS idx_model_capability_profile_audit_canonical_created
    ON model_capability_profile_audit (canonical_id, created_at DESC);

CREATE TABLE IF NOT EXISTS model_substitution_overrides (
    requested_canonical_id BIGINT NOT NULL REFERENCES models_canonical(id) ON DELETE CASCADE,
    candidate_canonical_id BIGINT NOT NULL REFERENCES models_canonical(id) ON DELETE CASCADE,
    action TEXT NOT NULL,
    reason TEXT,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (requested_canonical_id, candidate_canonical_id),
    CONSTRAINT model_substitution_overrides_action_check CHECK (action IN ('allow', 'deny')),
    CONSTRAINT model_substitution_overrides_distinct_models CHECK (requested_canonical_id <> candidate_canonical_id)
);

COMMENT ON TABLE model_capability_profiles IS
    '342: canonical model capability profile for shadow-only substitution analysis; runtime availability remains binding-scoped.';
COMMENT ON TABLE model_substitution_overrides IS
    '342: explicit canonical substitution allow/deny. deny takes precedence over automatic similarity.';

COMMIT;
