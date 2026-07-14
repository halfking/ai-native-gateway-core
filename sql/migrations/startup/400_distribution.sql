-- 400_distribution.sql — License holders, download events, donations (2026-07-14)

CREATE TABLE IF NOT EXISTS license_holders (
    id              BIGSERIAL PRIMARY KEY,
    email           TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL DEFAULT '',
    holder_type     TEXT NOT NULL DEFAULT 'individual'
        CHECK (holder_type IN ('individual', 'organization')),
    consent_version TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_license_holders_email_lower
    ON license_holders (lower(email));

ALTER TABLE licenses
    ADD COLUMN IF NOT EXISTS holder_id BIGINT REFERENCES license_holders(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_licenses_holder ON licenses (holder_id)
    WHERE holder_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS download_events (
    id              BIGSERIAL PRIMARY KEY,
    request_id      TEXT NOT NULL UNIQUE,
    release_version TEXT NOT NULL,
    platform        TEXT NOT NULL,
    arch            TEXT NOT NULL DEFAULT '',
    edition         TEXT NOT NULL DEFAULT 'customer',
    channel         TEXT NOT NULL DEFAULT 'stable',
    holder_id       BIGINT REFERENCES license_holders(id) ON DELETE SET NULL,
    donation_id     BIGINT,
    result          TEXT NOT NULL DEFAULT 'started'
        CHECK (result IN ('started', 'completed', 'failed')),
    duration_ms     INT,
    source          TEXT NOT NULL DEFAULT 'web',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_download_events_created ON download_events (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_download_events_version ON download_events (release_version, created_at DESC);

CREATE TABLE IF NOT EXISTS donations (
    id              BIGSERIAL PRIMARY KEY,
    order_no        TEXT NOT NULL UNIQUE,
    holder_id       BIGINT REFERENCES license_holders(id) ON DELETE SET NULL,
    email           TEXT NOT NULL DEFAULT '',
    amount_cents    INT NOT NULL CHECK (amount_cents > 0),
    currency        TEXT NOT NULL DEFAULT 'CNY',
    channel         TEXT NOT NULL DEFAULT 'alipay'
        CHECK (channel IN ('alipay', 'wechat', 'manual')),
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'paid', 'cancelled', 'expired')),
    tier_label      TEXT NOT NULL DEFAULT 'supporter',
    paid_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_donations_status ON donations (status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_donations_email ON donations (lower(email));

CREATE TABLE IF NOT EXISTS release_artifacts (
    id              BIGSERIAL PRIMARY KEY,
    release_version TEXT NOT NULL,
    platform        TEXT NOT NULL,
    arch            TEXT NOT NULL DEFAULT '',
    edition         TEXT NOT NULL DEFAULT 'customer',
    artifact_name   TEXT NOT NULL,
    sha256          TEXT NOT NULL DEFAULT '',
    size_bytes      BIGINT NOT NULL DEFAULT 0,
    download_path   TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (release_version, platform, arch, edition, artifact_name)
);
