-- 536_stats_analytics_foundation.sql
-- Canonical, replayable statistics foundation. No request/session bodies are stored here.

BEGIN;

CREATE TABLE IF NOT EXISTS stats_event_dedup (
    event_id       text PRIMARY KEY,
    occurred_at    timestamptz NOT NULL,
    first_seen_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS stats_event_inbox (
    event_id          text NOT NULL,
    occurred_at       timestamptz NOT NULL,
    request_id        text NOT NULL,
    event_type        text NOT NULL,
    traffic_class     text NOT NULL DEFAULT 'unknown',
    attempt_no        integer NOT NULL DEFAULT 0,
    tenant_id         text NOT NULL DEFAULT 'default',
    provider_id       bigint,
    credential_id     bigint,
    canonical_id      bigint,
    raw_model_name    text,
    api_key_id        bigint,
    application_id    bigint,
    end_user_id       text,
    person_hash       text,
    client_profile    text,
    agent_name        text,
    virtual_client_id text,
    identity_hash     text,
    status            text NOT NULL,
    error_kind        text,
    error_class       text,
    error_code        text,
    failure_stage     text,
    attribution_owner text,
    http_status       integer,
    retryable         boolean NOT NULL DEFAULT false,
    prompt_tokens     bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    cache_read_tokens bigint NOT NULL DEFAULT 0,
    cache_write_tokens bigint NOT NULL DEFAULT 0,
    reasoning_tokens  bigint NOT NULL DEFAULT 0,
    image_tokens      bigint NOT NULL DEFAULT 0,
    audio_tokens      bigint NOT NULL DEFAULT 0,
    video_tokens      bigint NOT NULL DEFAULT 0,
    provider_tokens   bigint NOT NULL DEFAULT 0,
    total_tokens      bigint NOT NULL DEFAULT 0,
    cost_usd          numeric(20,8) NOT NULL DEFAULT 0,
    cost_currency     text,
    credits_charged   bigint NOT NULL DEFAULT 0,
    usage_source      text,
    pricing_version   text,
    latency_ms        bigint NOT NULL DEFAULT 0,
    ttft_ms           bigint NOT NULL DEFAULT 0,
    source            text NOT NULL DEFAULT 'unknown',
    payload_version   integer NOT NULL DEFAULT 1,
    processed_at      timestamptz,
    processing_owner  text,
    lease_until       timestamptz,
    process_attempts  integer NOT NULL DEFAULT 0,
    last_error        text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE TABLE IF NOT EXISTS stats_event_inbox_default
    PARTITION OF stats_event_inbox DEFAULT;

CREATE INDEX IF NOT EXISTS idx_stats_event_inbox_pending
    ON stats_event_inbox (occurred_at, created_at)
    WHERE processed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_stats_event_inbox_request
    ON stats_event_inbox (request_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_stats_event_inbox_scope
    ON stats_event_inbox (tenant_id, occurred_at DESC, event_type);

CREATE TABLE IF NOT EXISTS stats_usage_daily (
    day_utc           date NOT NULL,
    tenant_id         text NOT NULL DEFAULT 'default',
    provider_id       bigint NOT NULL DEFAULT 0,
    credential_id     bigint NOT NULL DEFAULT 0,
    canonical_id      bigint NOT NULL DEFAULT 0,
    raw_model_name    text NOT NULL DEFAULT '',
    dimension_type    text NOT NULL DEFAULT 'provider_model',
    dimension_key     text NOT NULL DEFAULT '',
    traffic_class     text NOT NULL DEFAULT 'business',
    request_count     bigint NOT NULL DEFAULT 0,
    success_count     bigint NOT NULL DEFAULT 0,
    failure_count     bigint NOT NULL DEFAULT 0,
    timeout_count     bigint NOT NULL DEFAULT 0,
    rate_limited_count bigint NOT NULL DEFAULT 0,
    attempt_count     bigint NOT NULL DEFAULT 0,
    retry_count       bigint NOT NULL DEFAULT 0,
    probe_count       bigint NOT NULL DEFAULT 0,
    switch_count      bigint NOT NULL DEFAULT 0,
    prompt_tokens     bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    cache_read_tokens bigint NOT NULL DEFAULT 0,
    cache_write_tokens bigint NOT NULL DEFAULT 0,
    reasoning_tokens  bigint NOT NULL DEFAULT 0,
    image_tokens      bigint NOT NULL DEFAULT 0,
    audio_tokens      bigint NOT NULL DEFAULT 0,
    video_tokens      bigint NOT NULL DEFAULT 0,
    provider_tokens   bigint NOT NULL DEFAULT 0,
    total_tokens      bigint NOT NULL DEFAULT 0,
    credits_charged   bigint NOT NULL DEFAULT 0,
    cost_usd          numeric(20,8) NOT NULL DEFAULT 0,
    latency_count     bigint NOT NULL DEFAULT 0,
    latency_sum_ms    bigint NOT NULL DEFAULT 0,
    ttft_count        bigint NOT NULL DEFAULT 0,
    ttft_sum_ms       bigint NOT NULL DEFAULT 0,
    source_event_count bigint NOT NULL DEFAULT 0,
    source_max_occurred_at timestamptz,
    rollup_version    integer NOT NULL DEFAULT 1,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (day_utc, tenant_id, provider_id, credential_id, canonical_id, raw_model_name, dimension_type, dimension_key, traffic_class)
);
CREATE INDEX IF NOT EXISTS idx_stats_usage_daily_tenant ON stats_usage_daily (tenant_id, day_utc DESC);
CREATE INDEX IF NOT EXISTS idx_stats_usage_daily_provider ON stats_usage_daily (provider_id, day_utc DESC);
CREATE INDEX IF NOT EXISTS idx_stats_usage_daily_model ON stats_usage_daily (canonical_id, day_utc DESC);

CREATE TABLE IF NOT EXISTS stats_usage_monthly (
    month_start       date NOT NULL,
    tenant_id         text NOT NULL DEFAULT 'default',
    provider_id       bigint NOT NULL DEFAULT 0,
    credential_id     bigint NOT NULL DEFAULT 0,
    canonical_id      bigint NOT NULL DEFAULT 0,
    raw_model_name    text NOT NULL DEFAULT '',
    dimension_type    text NOT NULL DEFAULT 'provider_model',
    dimension_key     text NOT NULL DEFAULT '',
    traffic_class     text NOT NULL DEFAULT 'business',
    request_count     bigint NOT NULL DEFAULT 0,
    success_count     bigint NOT NULL DEFAULT 0,
    failure_count     bigint NOT NULL DEFAULT 0,
    timeout_count     bigint NOT NULL DEFAULT 0,
    rate_limited_count bigint NOT NULL DEFAULT 0,
    attempt_count     bigint NOT NULL DEFAULT 0,
    retry_count       bigint NOT NULL DEFAULT 0,
    probe_count       bigint NOT NULL DEFAULT 0,
    switch_count      bigint NOT NULL DEFAULT 0,
    prompt_tokens     bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    cache_read_tokens bigint NOT NULL DEFAULT 0,
    cache_write_tokens bigint NOT NULL DEFAULT 0,
    reasoning_tokens  bigint NOT NULL DEFAULT 0,
    image_tokens      bigint NOT NULL DEFAULT 0,
    audio_tokens      bigint NOT NULL DEFAULT 0,
    video_tokens      bigint NOT NULL DEFAULT 0,
    provider_tokens   bigint NOT NULL DEFAULT 0,
    total_tokens      bigint NOT NULL DEFAULT 0,
    credits_charged   bigint NOT NULL DEFAULT 0,
    cost_usd          numeric(20,8) NOT NULL DEFAULT 0,
    latency_count     bigint NOT NULL DEFAULT 0,
    latency_sum_ms    bigint NOT NULL DEFAULT 0,
    ttft_count        bigint NOT NULL DEFAULT 0,
    ttft_sum_ms       bigint NOT NULL DEFAULT 0,
    source_day_count  integer NOT NULL DEFAULT 0,
    source_high_watermark timestamptz,
    pricing_version   text,
    row_checksum      text NOT NULL DEFAULT '',
    status            text NOT NULL DEFAULT 'open' CHECK (status IN ('open','closing','closed','adjusted')),
    closed_at         timestamptz,
    rollup_version    integer NOT NULL DEFAULT 1,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (month_start, tenant_id, provider_id, credential_id, canonical_id, raw_model_name, dimension_type, dimension_key, traffic_class)
);
CREATE INDEX IF NOT EXISTS idx_stats_usage_monthly_tenant ON stats_usage_monthly (tenant_id, month_start DESC);
CREATE INDEX IF NOT EXISTS idx_stats_usage_monthly_status ON stats_usage_monthly (status, month_start DESC);

CREATE TABLE IF NOT EXISTS stats_adjustments (
    id               bigserial PRIMARY KEY,
    adjustment_id    text NOT NULL UNIQUE,
    month_start      date NOT NULL,
    tenant_id        text NOT NULL,
    dimension_type    text NOT NULL,
    dimension_key     text NOT NULL,
    metric           text NOT NULL,
    delta            numeric(30,8) NOT NULL,
    currency         text,
    reason           text NOT NULL,
    source_event_id  text,
    approved_by      text,
    approved_at      timestamptz,
    created_by       text NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_stats_adjustments_month ON stats_adjustments (month_start, tenant_id);

CREATE TABLE IF NOT EXISTS stats_reconciliation_runs (
    id               bigserial PRIMARY KEY,
    run_id            text NOT NULL UNIQUE,
    period_start     timestamptz NOT NULL,
    period_end       timestamptz NOT NULL,
    scope             text NOT NULL DEFAULT 'all',
    status            text NOT NULL DEFAULT 'running',
    source_watermark  timestamptz,
    events_seen       bigint NOT NULL DEFAULT 0,
    rows_compared     bigint NOT NULL DEFAULT 0,
    rows_repaired     bigint NOT NULL DEFAULT 0,
    diff_count        bigint NOT NULL DEFAULT 0,
    error             text,
    started_at        timestamptz NOT NULL DEFAULT now(),
    finished_at       timestamptz
);

CREATE TABLE IF NOT EXISTS stats_reconciliation_diffs (
    id               bigserial PRIMARY KEY,
    run_id            text NOT NULL REFERENCES stats_reconciliation_runs(run_id) ON DELETE CASCADE,
    dimension_type    text NOT NULL,
    dimension_key     text NOT NULL,
    metric            text NOT NULL,
    source_value      numeric(30,8) NOT NULL DEFAULT 0,
    projected_value   numeric(30,8) NOT NULL DEFAULT 0,
    difference        numeric(30,8) NOT NULL DEFAULT 0,
    resolution       text NOT NULL DEFAULT 'open',
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_stats_reconciliation_diffs_run ON stats_reconciliation_diffs (run_id, resolution);

COMMENT ON TABLE stats_event_inbox IS 'Compact, body-free statistics event inbox. Replayable and idempotent by event_id.';
COMMENT ON TABLE stats_usage_daily IS 'Rebuildable daily usage projection. UTC day buckets.';
COMMENT ON TABLE stats_usage_monthly IS 'Auditable monthly usage projection. Closed periods require adjustments, not overwrites.';

COMMIT;
