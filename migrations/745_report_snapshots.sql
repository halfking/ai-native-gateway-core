-- report_snapshots: daily report snapshots for provider reconciliation & internal billing.
-- One row per (scope, scope_key, report_date); idempotent upsert on rerun.
-- price_snapshot freezes internal_*/external_* prices at snapshot time so historical
-- reports do not drift when provider_model_prices is adjusted later.

CREATE TABLE IF NOT EXISTS report_snapshots (
    id                  BIGSERIAL PRIMARY KEY,
    scope               TEXT NOT NULL,        -- daily_total | daily_by_provider | daily_by_model | internal_tenant
    scope_key           TEXT NOT NULL,        -- for *_by_* : provider_id/canonical_id/tenant_id; for daily_total : 'all'
    report_date         DATE NOT NULL,        -- UTC day the snapshot covers
    granularity         TEXT NOT NULL DEFAULT 'day',  -- reserved for future week/month
    request_count       BIGINT NOT NULL DEFAULT 0,
    success_count       BIGINT NOT NULL DEFAULT 0,
    error_count         BIGINT NOT NULL DEFAULT 0,
    input_tokens        BIGINT NOT NULL DEFAULT 0,
    output_tokens       BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens   BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens  BIGINT NOT NULL DEFAULT 0,
    error_kind_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb,
    cache_hit_ratio     NUMERIC(6,4)          -- cache_read / (input_tokens+cache_read); nullable when denom=0
                            CHECK (cache_hit_ratio IS NULL OR (cache_hit_ratio >= 0 AND cache_hit_ratio <= 1)),
    estimated_cost_cents BIGINT NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT 'USD',
    price_snapshot      JSONB NOT NULL DEFAULT '{}'::jsonb,
    provider_id         BIGINT,               -- nullable; set for *_by_provider
    canonical_id        BIGINT,               -- nullable; set for *_by_model / internal_tenant
    tenant_id           BIGINT,               -- nullable; set for internal_tenant
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (scope, scope_key, report_date)
);

CREATE INDEX IF NOT EXISTS idx_report_snapshots_scope_date
    ON report_snapshots (scope, report_date DESC);

CREATE INDEX IF NOT EXISTS idx_report_snapshots_date
    ON report_snapshots (report_date DESC);