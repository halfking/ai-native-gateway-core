-- Explicit per-instance consent for allowlisted operational telemetry only.
CREATE TABLE IF NOT EXISTS runtime_telemetry_preferences (
    hardware_hash     TEXT PRIMARY KEY,
    license_id        BIGINT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    enabled           BOOLEAN NOT NULL DEFAULT FALSE,
    agreement_version TEXT NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    disabled_at       TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_runtime_telemetry_preferences_license
    ON runtime_telemetry_preferences (license_id);

CREATE TABLE IF NOT EXISTS runtime_telemetry_consent_events (
    id                BIGSERIAL PRIMARY KEY,
    hardware_hash     TEXT NOT NULL,
    license_id        BIGINT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    enabled           BOOLEAN NOT NULL,
    agreement_version TEXT NOT NULL,
    operator_user_id  BIGINT NOT NULL,
    source            TEXT NOT NULL,
    occurred_at       TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runtime_telemetry_consent_events_hardware_time
    ON runtime_telemetry_consent_events (hardware_hash, occurred_at DESC);
