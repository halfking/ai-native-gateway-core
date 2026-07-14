-- Immutable audit evidence for explicit Trial agreement acceptance.
CREATE TABLE IF NOT EXISTS license_trial_consents (
    id                BIGSERIAL PRIMARY KEY,
    license_id        BIGINT NOT NULL UNIQUE REFERENCES licenses(id) ON DELETE CASCADE,
    agreement_version TEXT NOT NULL,
    accepted_at       TIMESTAMPTZ NOT NULL,
    source            TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_license_trial_consents_accepted_at
    ON license_trial_consents (accepted_at DESC);
