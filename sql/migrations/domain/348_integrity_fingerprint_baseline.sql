-- Migration 348: integrity_fingerprint_baseline
--
-- Stores per-(credential, model) historical baseline fingerprints so the
-- drift worker compares today's dominant fingerprint against a stable
-- baseline (rather than just measuring fragmentation within the current
-- window). Synthetic / probe traffic is filtered out of the baseline
-- sample so a noisy probe run does not poison the production reference.
--
-- The drift worker still has an in-tick dedup via this table's
-- last_alerted_at + last_alerted_fingerprint pair, eliminating the
-- "every hourly tick produces a duplicate event" regression in the
-- previous implementation.
BEGIN;

CREATE TABLE IF NOT EXISTS integrity_fingerprint_baseline (
    tenant_id              TEXT NOT NULL DEFAULT 'default',
    provider_id            INT,
    credential_id          INT NOT NULL,
    raw_model_name         TEXT NOT NULL,
    baseline_fingerprint   TEXT,
    baseline_share_pct     INT,
    baseline_sample_count  BIGINT NOT NULL DEFAULT 0,
    baseline_window_start  TIMESTAMPTZ,
    baseline_window_end    TIMESTAMPTZ,
    current_fingerprint    TEXT,
    current_share_pct      INT,
    last_observed_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_alerted_fingerprint TEXT,
    last_alerted_at        TIMESTAMPTZ,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, credential_id, raw_model_name)
);

CREATE INDEX IF NOT EXISTS idx_integrity_fingerprint_baseline_cred_model
    ON integrity_fingerprint_baseline (credential_id, raw_model_name);

COMMENT ON TABLE integrity_fingerprint_baseline IS
'348: per-(cred, model) historical baseline vs current dominant fingerprint; source of truth for fingerprint_drift events.';

COMMIT;
