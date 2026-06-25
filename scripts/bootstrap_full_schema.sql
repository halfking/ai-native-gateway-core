-- ===========================================================================
-- scripts/bootstrap_full_schema.sql
-- 完整的 schema bootstrap：创建 request_logs、request_wal 等核心表
-- 在本地 DB 上运行，让 gateway 服务可以正常记录请求日志
-- Idempotent：使用 IF NOT EXISTS
-- ===========================================================================

-- ============= request_logs 主表 =============
CREATE TABLE IF NOT EXISTS request_logs (
    id                       BIGSERIAL,
    request_id               TEXT NOT NULL,
    ts                       TIMESTAMPTZ NOT NULL DEFAULT now(),
    tenant_id                TEXT NOT NULL DEFAULT 'default',
    application_id           INT,
    api_key_id               INT,
    end_user_id              TEXT,
    client_model             TEXT,
    outbound_model           TEXT,
    credential_id            INT,
    provider_id              INT,
    canonical_id             INT,
    client_profile           TEXT,
    request_mode             TEXT,
    prompt_tokens            INT,
    completion_tokens        INT,
    cache_read_tokens        INT,
    cache_write_tokens       INT,
    total_tokens             INT,
    cost_usd                 NUMERIC(12, 6),
    cost_display             NUMERIC(12, 4),
    cost_currency            TEXT,
    latency_ms               INT,
    success                  BOOLEAN,
    request_status           TEXT,
    error_kind               TEXT,
    search_text              TEXT,
    identity_hash            TEXT,
    response_checksum        TEXT,
    transform_rule_id        TEXT,
    egress_protocol          TEXT,
    failure_stage            TEXT,
    failure_detail_code      TEXT,
    request_preview          TEXT,
    transform_summary        TEXT,
    response_preview         TEXT,
    request_body             JSONB,
    response_body            JSONB,
    stream_first_chunk_ms    INT,
    stream_chunk_count       INT,
    stream_done_received     BOOLEAN,
    stream_interrupted       BOOLEAN,
    usage_source             TEXT,
    gw_session_id            TEXT,
    gw_task_id               TEXT,
    api_key_prefix           TEXT,
    api_key_owner_user       TEXT,
    application_code         TEXT,
    is_auto_request          BOOLEAN DEFAULT FALSE,
    task_type                TEXT,
    auto_profile             TEXT,
    auto_decision            JSONB,
    auto_confidence          NUMERIC(5, 4),
    work_type                TEXT,
    credits_charged          BIGINT,
    parent_request_id        TEXT,
    compression_reason       TEXT,
    compression_strategy     TEXT,
    compression_meta         JSONB,
    outbound_body            JSONB,
    outbound_msg_count       INT,
    outbound_token_est       INT,
    outbound_msg_hashes      JSONB,
    quality_flags            TEXT[] NOT NULL DEFAULT '{}',
    quality_fix_actions      JSONB NOT NULL DEFAULT '{}'::jsonb,
    quality_score            NUMERIC(3, 2),
    upstream_finish_reason   TEXT,
    tool_calls               JSONB,
    PRIMARY KEY (id, ts)
);

-- 分区主键索引
CREATE INDEX IF NOT EXISTS idx_request_logs_request_id
    ON request_logs (request_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_request_logs_request_id_unique
    ON request_logs (request_id);
CREATE INDEX IF NOT EXISTS idx_request_logs_ts
    ON request_logs (ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_tenant_ts
    ON request_logs (tenant_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_provider_ts
    ON request_logs (provider_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_credential_ts
    ON request_logs (credential_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_status_ts
    ON request_logs (request_status, ts DESC)
    WHERE request_status IS NOT NULL AND request_status <> '';
CREATE INDEX IF NOT EXISTS idx_request_logs_gw_session_ts
    ON request_logs (gw_session_id, ts DESC)
    WHERE gw_session_id IS NOT NULL AND gw_session_id <> '';
CREATE INDEX IF NOT EXISTS idx_request_logs_gw_task_ts
    ON request_logs (gw_task_id, ts DESC)
    WHERE gw_task_id IS NOT NULL AND gw_task_id <> '';
CREATE INDEX IF NOT EXISTS idx_request_logs_parent_ts
    ON request_logs (parent_request_id, ts DESC)
    WHERE parent_request_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_logs_session_outbound
    ON request_logs (gw_session_id, ts DESC)
    WHERE gw_session_id IS NOT NULL AND outbound_body IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_logs_outbound_msg_count
    ON request_logs (tenant_id, ts DESC)
    WHERE outbound_msg_count IS NOT NULL AND outbound_msg_count > 0;
CREATE INDEX IF NOT EXISTS idx_request_logs_credits_charged
    ON request_logs (tenant_id, ts DESC)
    WHERE credits_charged IS NOT NULL AND credits_charged > 0;
CREATE INDEX IF NOT EXISTS idx_request_logs_work_type
    ON request_logs (work_type, ts DESC)
    WHERE work_type IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_logs_quality_flags
    ON request_logs USING GIN (quality_flags)
    WHERE cardinality(quality_flags) > 0;
CREATE INDEX IF NOT EXISTS idx_request_logs_provider_quality
    ON request_logs (provider_id, quality_score, ts DESC)
    WHERE quality_score IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_logs_upstream_finish_reason
    ON request_logs (upstream_finish_reason, ts DESC)
    WHERE upstream_finish_reason IS NOT NULL AND upstream_finish_reason <> '';
CREATE INDEX IF NOT EXISTS idx_request_logs_tool_calls
    ON request_logs USING GIN (tool_calls)
    WHERE tool_calls IS NOT NULL AND tool_calls != '[]'::jsonb;
CREATE INDEX IF NOT EXISTS idx_request_logs_provider_tool_calls
    ON request_logs (provider_id, ts DESC)
    WHERE tool_calls IS NOT NULL AND jsonb_array_length(tool_calls) > 0;

-- ============= request_wal 新 WAL 表 =============
CREATE TABLE IF NOT EXISTS request_wal (
    request_id              TEXT NOT NULL,
    tenant_id               TEXT NOT NULL DEFAULT 'default',
    gw_session_id           TEXT,
    status                  TEXT NOT NULL DEFAULT 'pending',
    stage                   INT NOT NULL DEFAULT 0,
    client_model            TEXT,
    upstream_provider_id    INT,
    upstream_credential_id  INT,
    completion_tokens       INT,
    prompt_tokens           INT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at            TIMESTAMPTZ,
    upstream_request_at     TIMESTAMPTZ,
    upstream_response_at    TIMESTAMPTZ,
    error                   TEXT,
    compression_strategy    TEXT,
    compression_meta        JSONB,
    PRIMARY KEY (request_id, created_at)
);
CREATE INDEX IF NOT EXISTS idx_request_wal_tenant_ts
    ON request_wal (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_wal_status_ts
    ON request_wal (status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_wal_session_ts
    ON request_wal (gw_session_id, created_at DESC)
    WHERE gw_session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_wal_provider_ts
    ON request_wal (upstream_provider_id, created_at DESC);

CREATE TABLE IF NOT EXISTS request_wal_bodies (
    request_id   TEXT PRIMARY KEY,
    request_body JSONB,
    response_body JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============= candidate_failure_logs（路由失败审计）=============
CREATE TABLE IF NOT EXISTS candidate_failure_logs (
    id                     BIGSERIAL PRIMARY KEY,
    request_id             TEXT NOT NULL,
    ts                     TIMESTAMPTZ NOT NULL DEFAULT now(),
    tenant_id              TEXT NOT NULL DEFAULT 'default',
    provider_id            INT,
    credential_id          INT,
    raw_model_name         TEXT,
    standardized_name      TEXT,
    outbound_model_name    TEXT,
    routing_tier           INT,
    failure_stage          TEXT,
    failure_kind           TEXT,
    failure_status_code    INT,
    failure_error_message  TEXT,
    failure_detail_code    TEXT,
    attempt_number         INT,
    per_attempt_latency_ms INT,
    upstream_finish_reason TEXT,
    work_type              TEXT,
    is_routable            BOOLEAN,
    gw_session_id          TEXT
);
CREATE INDEX IF NOT EXISTS idx_candidate_failure_logs_request_id_ts
    ON candidate_failure_logs (request_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_candidate_failure_logs_provider_ts
    ON candidate_failure_logs (provider_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_candidate_failure_logs_credential_ts
    ON candidate_failure_logs (credential_id, ts DESC);

-- ============= work_type_config 和 work_type_model_route =============
CREATE TABLE IF NOT EXISTS work_type_config (
    key                 TEXT PRIMARY KEY,
    label               TEXT NOT NULL,
    category            TEXT NOT NULL,
    l1_task_type        TEXT NOT NULL,
    default_profile     TEXT NOT NULL DEFAULT 'smart'
                            CHECK (default_profile IN ('smart', 'speed_first', 'cost_first')),
    tags                TEXT[] NOT NULL DEFAULT '{}',
    prompt_keywords     TEXT[] NOT NULL DEFAULT '{}',
    acc_task_type       TEXT,
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order          INT NOT NULL DEFAULT 0,
    synced_from_acc_at  TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    system_prompt       TEXT
);
CREATE INDEX IF NOT EXISTS idx_work_type_config_category ON work_type_config (category, sort_order);
CREATE INDEX IF NOT EXISTS idx_work_type_config_l1 ON work_type_config (l1_task_type);

CREATE TABLE IF NOT EXISTS work_type_model_route (
    id              SERIAL PRIMARY KEY,
    work_type_key   TEXT NOT NULL REFERENCES work_type_config(key) ON DELETE CASCADE,
    canonical_name  TEXT NOT NULL,
    weight          NUMERIC(5,2) NOT NULL DEFAULT 1.0,
    min_score       NUMERIC(8,4) NOT NULL DEFAULT 0,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    UNIQUE (work_type_key, canonical_name)
);
CREATE INDEX IF NOT EXISTS idx_wtmr_work_type ON work_type_model_route (work_type_key);

-- ============= users =============
CREATE TABLE IF NOT EXISTS users (
    id              SERIAL PRIMARY KEY,
    tenant_id       VARCHAR(64) NOT NULL DEFAULT 'default',
    username        VARCHAR(128) NOT NULL UNIQUE,
    password_hash   VARCHAR(256) NOT NULL,
    display_name    VARCHAR(128) NOT NULL DEFAULT '',
    email           VARCHAR(256) NOT NULL DEFAULT '',
    role            VARCHAR(32) NOT NULL DEFAULT 'tenant_admin',
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_users_tenant ON users(tenant_id);

-- ============= tenants =============
CREATE TABLE IF NOT EXISTS tenants (
    id              SERIAL PRIMARY KEY,
    code            VARCHAR(64) NOT NULL UNIQUE,
    display_name    VARCHAR(128) NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============= applications（API key 管理）=============
CREATE TABLE IF NOT EXISTS applications (
    id              SERIAL PRIMARY KEY,
    tenant_id       TEXT NOT NULL DEFAULT 'default',
    code            TEXT NOT NULL,
    name            TEXT NOT NULL,
    api_key_hash    TEXT,
    api_key_prefix  TEXT,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    metadata        JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, code)
);
CREATE INDEX IF NOT EXISTS idx_applications_api_key_prefix
    ON applications (api_key_prefix)
    WHERE api_key_prefix IS NOT NULL;

-- ============= fp_slot_limit =============
CREATE TABLE IF NOT EXISTS fp_slot_limit (
    credential_id   INT PRIMARY KEY,
    max_concurrency INT NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============= credential_model_call_history =============
CREATE TABLE IF NOT EXISTS credential_model_call_history (
    id             BIGSERIAL PRIMARY KEY,
    credential_id  INT NOT NULL,
    raw_model_name TEXT NOT NULL,
    ts             TIMESTAMPTZ NOT NULL DEFAULT now(),
    success        BOOLEAN NOT NULL,
    latency_ms     INT,
    status_code    INT,
    error_kind     TEXT,
    request_id     TEXT
);
CREATE INDEX IF NOT EXISTS idx_credential_model_call_history_cred_model_ts
    ON credential_model_call_history (credential_id, raw_model_name, ts DESC);

-- ============= tuning_signals + tuning_proposals =============
CREATE TABLE IF NOT EXISTS tuning_signals (
    id           BIGSERIAL PRIMARY KEY,
    ts           TIMESTAMPTZ NOT NULL DEFAULT now(),
    signal_type  TEXT NOT NULL,
    payload      JSONB NOT NULL,
    processed    BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS idx_tuning_signals_ts ON tuning_signals (ts DESC);

CREATE TABLE IF NOT EXISTS tuning_proposals (
    id           BIGSERIAL PRIMARY KEY,
    ts           TIMESTAMPTZ NOT NULL DEFAULT now(),
    proposal_type TEXT NOT NULL,
    payload      JSONB NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    approved_at  TIMESTAMPTZ,
    approved_by  TEXT
);

-- ============= routing_overrides =============
CREATE TABLE IF NOT EXISTS routing_overrides (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       TEXT NOT NULL DEFAULT 'default',
    raw_model_name  TEXT NOT NULL,
    canonical_name  TEXT,
    override_credential_id INT,
    override_provider_id   INT,
    reason          TEXT,
    expires_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    enabled         BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE TABLE IF NOT EXISTS routing_overrides_audit (
    id              BIGSERIAL PRIMARY KEY,
    override_id     BIGINT,
    action          TEXT NOT NULL,
    actor           TEXT,
    payload         JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============= tenant_model_policies =============
CREATE TABLE IF NOT EXISTS tenant_model_policies (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       TEXT NOT NULL DEFAULT 'default',
    raw_model_name  TEXT NOT NULL,
    policy_type     TEXT NOT NULL,
    policy_value    JSONB NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, raw_model_name, policy_type)
);

CREATE TABLE IF NOT EXISTS tenant_model_policies_audit (
    id              BIGSERIAL PRIMARY KEY,
    policy_id       BIGINT,
    action          TEXT NOT NULL,
    actor           TEXT,
    payload         JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============= passive_probe_state =============
CREATE TABLE IF NOT EXISTS passive_probe_state (
    id              BIGSERIAL PRIMARY KEY,
    provider_id     INT NOT NULL,
    credential_id   INT NOT NULL,
    raw_model_name  TEXT NOT NULL,
    ts              TIMESTAMPTZ NOT NULL DEFAULT now(),
    success         BOOLEAN NOT NULL,
    latency_ms      INT,
    status_code     INT,
    error_kind      TEXT
);
CREATE INDEX IF NOT EXISTS idx_passive_probe_state_cred_model_ts
    ON passive_probe_state (credential_id, raw_model_name, ts DESC);

-- ============= 报告 =============
DO $$ BEGIN
    RAISE NOTICE 'Bootstrap schema completed: request_logs=% , request_wal=% , candidate_failure_logs=%',
        (SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'request_logs'),
        (SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'request_wal'),
        (SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'candidate_failure_logs');
END $$;