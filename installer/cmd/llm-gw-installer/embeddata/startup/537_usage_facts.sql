-- 537_usage_facts.sql
-- Canonical request-level usage facts. One terminal business outcome per request/revision.

BEGIN;

CREATE TABLE IF NOT EXISTS usage_facts (
    fact_id            bigserial,
    event_id           text NOT NULL,
    request_id         text NOT NULL,
    revision           integer NOT NULL DEFAULT 1,
    occurred_at        timestamptz NOT NULL,
    finalized_at       timestamptz NOT NULL DEFAULT now(),
    tenant_id          text NOT NULL DEFAULT 'default',
    traffic_class      text NOT NULL DEFAULT 'business',
    status             text NOT NULL,
    provider_id        bigint,
    credential_id      bigint,
    canonical_id       bigint,
    raw_model_name     text,
    api_key_id         bigint,
    application_id     bigint,
    end_user_id        text,
    person_hash        text,
    prompt_tokens      bigint NOT NULL DEFAULT 0,
    completion_tokens  bigint NOT NULL DEFAULT 0,
    cache_read_tokens  bigint NOT NULL DEFAULT 0,
    cache_write_tokens bigint NOT NULL DEFAULT 0,
    reasoning_tokens   bigint NOT NULL DEFAULT 0,
    image_tokens       bigint NOT NULL DEFAULT 0,
    audio_tokens       bigint NOT NULL DEFAULT 0,
    video_tokens       bigint NOT NULL DEFAULT 0,
    provider_tokens    bigint NOT NULL DEFAULT 0,
    total_tokens       bigint NOT NULL DEFAULT 0,
    cost_amount        numeric(20,8) NOT NULL DEFAULT 0,
    cost_currency      text,
    credits_charged    bigint NOT NULL DEFAULT 0,
    usage_source       text,
    pricing_version    text,
    latency_ms         bigint NOT NULL DEFAULT 0,
    ttft_ms            bigint NOT NULL DEFAULT 0,
    error_kind         text,
    error_class        text,
    failure_stage      text,
    source             text NOT NULL DEFAULT 'stats_event_inbox',
    created_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (fact_id, occurred_at),
    UNIQUE (event_id, revision, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE TABLE IF NOT EXISTS usage_facts_default
    PARTITION OF usage_facts DEFAULT;

CREATE INDEX IF NOT EXISTS idx_usage_facts_request
    ON usage_facts (request_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_facts_tenant_time
    ON usage_facts (tenant_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_facts_provider_time
    ON usage_facts (provider_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_facts_model_time
    ON usage_facts (canonical_id, occurred_at DESC);

COMMENT ON TABLE usage_facts IS 'Canonical body-free request usage facts. Corrections append a higher revision instead of overwriting closed history.';
COMMENT ON COLUMN usage_facts.event_id IS 'Stable statistics event id; joins back to stats_event_inbox without storing session content.';
COMMENT ON COLUMN usage_facts.person_hash IS 'Non-reversible person dimension used when end_user_id is unavailable.';

COMMIT;
