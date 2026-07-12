-- tests/local/seed/01-schema-bootstrap.sql
-- 从 prod 复制 base schema 简化版（覆盖 audit/credentials/providers/cmb/handoff
-- 等关键表），同时 seed 几条 fixture 数据用于本地测试。

CREATE TABLE IF NOT EXISTS credentials (
    id                     BIGSERIAL PRIMARY KEY,
    provider_id            BIGINT NOT NULL,
    tenant_id              TEXT NOT NULL DEFAULT 'default',
    label                  TEXT NOT NULL,
    status                 TEXT NOT NULL DEFAULT 'active',
    health_status          TEXT NOT NULL DEFAULT 'unknown',
    availability_state     TEXT NOT NULL DEFAULT 'ready',
    consecutive_failures   INTEGER NOT NULL DEFAULT 0,
    circuit_state          TEXT NOT NULL DEFAULT 'closed',
    fp_slot_limit          INTEGER NOT NULL DEFAULT 0,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS providers (
    id                     BIGSERIAL PRIMARY KEY,
    code                   TEXT NOT NULL,
    display_name           TEXT NOT NULL,
    base_url               TEXT NOT NULL,
    enabled                BOOLEAN NOT NULL DEFAULT TRUE,
    manual_disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    kind                   TEXT NOT NULL DEFAULT 'cloud',
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS credential_model_bindings (
    id                     BIGSERIAL PRIMARY KEY,
    credential_id          BIGINT NOT NULL REFERENCES credentials(id),
    provider_model_id      BIGINT,
    available              BOOLEAN NOT NULL DEFAULT TRUE,
    unavailable_reason     TEXT,
    unavailable_at         TIMESTAMPTZ,
    unavailable_recover_at TIMESTAMPTZ,
    admin_protected        BOOLEAN NOT NULL DEFAULT FALSE,
    consecutive_failures   INTEGER NOT NULL DEFAULT 0,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS provider_models (
    id                     BIGSERIAL PRIMARY KEY,
    raw_model_name         TEXT UNIQUE NOT NULL,
    outbound_model_name    TEXT,
    provider_id            BIGINT NOT NULL REFERENCES providers(id),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS api_keys (
    id                     BIGSERIAL PRIMARY KEY,
    key_hash               TEXT UNIQUE NOT NULL,
    key_prefix             TEXT NOT NULL,
    owner_user             TEXT,
    rate_limit_rpm         INTEGER NOT NULL DEFAULT 600,
    rate_limit_concurrent  INTEGER NOT NULL DEFAULT 20,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS handoff_logs (
    id                     BIGSERIAL PRIMARY KEY,
    session_id             TEXT NOT NULL,
    tenant_id              TEXT NOT NULL,
    trigger_reason         TEXT NOT NULL,
    tokens_at_handoff      INTEGER NOT NULL,
    context_window         INTEGER,
    handoff_prompt         TEXT,
    new_session_id         TEXT,
    summary_text           TEXT,
    summary_engine         TEXT,
    trigger_mode           TEXT,
    tokens_in_session      INTEGER,
    messages_in_session    INTEGER,
    skill_name             TEXT,
    duration_ms            INTEGER,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS request_logs (
    id                     BIGSERIAL,
    ts                     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    request_id             TEXT NOT NULL,
    client_model           TEXT,
    request_status         TEXT,
    success                BOOLEAN,
    error_kind             TEXT,
    PRIMARY KEY (id, ts)
) PARTITION BY RANGE (ts);

CREATE TABLE IF NOT EXISTS request_logs_default PARTITION OF request_logs DEFAULT;

INSERT INTO providers (id, code, display_name, base_url) VALUES
    (1, 'mock',    'Local Mock LLM',      'http://mock-llm:8000/v1'),
    (2, 'mock-secondary', 'Mock Secondary', 'http://mock-llm:8000/v1')
ON CONFLICT DO NOTHING;

INSERT INTO provider_models (id, raw_model_name, outbound_model_name, provider_id) VALUES
    (1, 'minimax-m3',            'mock-m3',           1),
    (2, 'gpt-5.6-luna',          'gpt-5.6-luna',      1),
    (3, 'gpt-5.6-terra',         'gpt-5.6-terra',     1),
    (4, 'claude-sonnet-5',       'claude-sonnet-5',   1),
    (5, 'gpt-4o',                'gpt-4o',            2)
ON CONFLICT DO NOTHING;

INSERT INTO credentials (id, provider_id, label, fp_slot_limit) VALUES
    (1, 1, 'mock-primary',  4),
    (2, 1, 'mock-mirror',   4),
    (3, 2, 'mock-secondary',3)
ON CONFLICT DO NOTHING;

INSERT INTO credential_model_bindings (credential_id, provider_model_id, available)
SELECT c.id, pm.id, TRUE
FROM credentials c CROSS JOIN provider_models pm
ON CONFLICT DO NOTHING;

INSERT INTO api_keys (key_hash, key_prefix, owner_user) VALUES
    ('local-test-hash', 'sk-loc', 'local-tester')
ON CONFLICT DO NOTHING;

SELECT setval('providers_id_seq',     (SELECT MAX(id) FROM providers));
SELECT setval('credentials_id_seq',   (SELECT MAX(id) FROM credentials));
SELECT setval('provider_models_id_seq',(SELECT MAX(id) FROM provider_models));
SELECT setval('api_keys_id_seq',       (SELECT MAX(id) FROM api_keys));
