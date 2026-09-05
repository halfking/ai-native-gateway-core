-- Migration: 641_local_shared_platform_schema_fixup
-- Purpose: 本地共享 PG 兼容性修复，补齐 RedClaw / ACC 本地容器接入 llm_gateway 时依赖的 schema。
-- Date: 2026-09-02
-- 背景：本地 llm-gateway-pg 被 llm-gateway、ACC、RedClaw 本地栈共享使用；
-- RedClaw 轮询 platform/workflow/integration/fencing schema，ACC 访问
-- task_assigner_* / mcp_registry，缺失时会持续刷 SQLSTATE 42P01。
-- 安全策略：只做 additive DDL；不 DROP/TRUNCATE/DELETE/UPDATE 业务数据；
-- 不修改已有角色密码；不套用 RedClaw 原始 FORCE RLS 迁移，避免本地
-- platform_app 未切换 bypass 角色时再次引入权限问题。
-- No down migration: 如需移除，请先走数据库破坏性变更流程。

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'platform_app') THEN
        CREATE ROLE platform_app NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'platform_migrator') THEN
        CREATE ROLE platform_migrator NOLOGIN BYPASSRLS;
    END IF;
END
$$;

CREATE SCHEMA IF NOT EXISTS platform;
CREATE SCHEMA IF NOT EXISTS workflow;
CREATE SCHEMA IF NOT EXISTS integration;
CREATE SCHEMA IF NOT EXISTS fencing;

GRANT USAGE ON SCHEMA platform, workflow, integration, fencing TO platform_app;
GRANT ALL ON SCHEMA platform, workflow, integration, fencing TO platform_migrator;

CREATE TABLE IF NOT EXISTS platform.schema_migrations (
    version     BIGINT PRIMARY KEY,
    description TEXT NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS platform.platform_outbox (
    outbox_id         UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id         VARCHAR(64) NOT NULL,
    event_id          VARCHAR(128) NOT NULL,
    event_type        VARCHAR(128) NOT NULL,
    aggregate_type    VARCHAR(128) NOT NULL,
    aggregate_id      VARCHAR(256) NOT NULL,
    aggregate_version BIGINT NOT NULL,
    payload           JSONB NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at      TIMESTAMPTZ,
    retry_count       INTEGER NOT NULL DEFAULT 0,
    next_attempt_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (created_at >= DATE '2024-01-01'),
    PRIMARY KEY (created_at, outbox_id)
) PARTITION BY RANGE (created_at);
CREATE TABLE IF NOT EXISTS platform.platform_outbox_default PARTITION OF platform.platform_outbox DEFAULT;
CREATE INDEX IF NOT EXISTS idx_outbox_event_id ON platform.platform_outbox (event_id);
CREATE INDEX IF NOT EXISTS idx_outbox_tenant ON platform.platform_outbox (tenant_id);
CREATE INDEX IF NOT EXISTS idx_outbox_unpublished ON platform.platform_outbox (created_at) WHERE published_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_outbox_retry_ready ON platform.platform_outbox (next_attempt_at, created_at) WHERE published_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_outbox_aggregate ON platform.platform_outbox (aggregate_type, aggregate_id, aggregate_version);

CREATE TABLE IF NOT EXISTS platform.platform_inbox (
    inbox_id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     VARCHAR(64) NOT NULL,
    event_id      VARCHAR(128) NOT NULL,
    event_type    VARCHAR(128) NOT NULL,
    consumer_id   VARCHAR(128) NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at  TIMESTAMPTZ,
    status        VARCHAR(32) NOT NULL CHECK (status IN ('received', 'processing', 'processed', 'failed')),
    UNIQUE (event_id, consumer_id, tenant_id)
);
CREATE INDEX IF NOT EXISTS idx_inbox_tenant ON platform.platform_inbox (tenant_id);
CREATE INDEX IF NOT EXISTS idx_inbox_consumer ON platform.platform_inbox (consumer_id, status);
CREATE INDEX IF NOT EXISTS idx_inbox_event ON platform.platform_inbox (event_id);
CREATE TABLE IF NOT EXISTS workflow.schema_migrations (
    version     BIGINT PRIMARY KEY,
    description TEXT NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS workflow.workflow_runs (
    run_id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          VARCHAR(64) NOT NULL,
    definition_id      VARCHAR(128) NOT NULL,
    definition_name    VARCHAR(128) NOT NULL,
    definition_version BIGINT NOT NULL,
    idempotency_key    VARCHAR(128) NOT NULL,
    status             VARCHAR(32) NOT NULL,
    aggregate_version  BIGINT NOT NULL DEFAULT 1,
    inputs             JSONB NOT NULL DEFAULT '{}'::jsonb,
    outputs            JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at         TIMESTAMPTZ,
    completed_at       TIMESTAMPTZ,
    expires_at         TIMESTAMPTZ,
    worker_id          VARCHAR(128) NOT NULL DEFAULT '',
    last_error         TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS wrun_tenant_idx ON workflow.workflow_runs (tenant_id);
CREATE INDEX IF NOT EXISTS wrun_status_idx ON workflow.workflow_runs (tenant_id, status);
CREATE INDEX IF NOT EXISTS wrun_def_idx ON workflow.workflow_runs (tenant_id, definition_id, definition_version);
CREATE TABLE IF NOT EXISTS workflow.node_runs (
    node_run_id       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id            UUID NOT NULL REFERENCES workflow.workflow_runs(run_id) ON DELETE CASCADE,
    tenant_id         VARCHAR(64) NOT NULL,
    node_id           VARCHAR(128) NOT NULL,
    node_kind         VARCHAR(32) NOT NULL,
    status            VARCHAR(32) NOT NULL,
    attempt           INTEGER NOT NULL DEFAULT 1,
    aggregate_version BIGINT NOT NULL DEFAULT 1,
    scheduled_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at        TIMESTAMPTZ,
    completed_at      TIMESTAMPTZ,
    fence_token       BIGINT NOT NULL,
    inputs            JSONB NOT NULL DEFAULT '{}'::jsonb,
    outputs           JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_message     TEXT NOT NULL DEFAULT '',
    idempotency_key   VARCHAR(128) NOT NULL DEFAULT '',
    UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS nrun_run_idx ON workflow.node_runs (run_id);
CREATE INDEX IF NOT EXISTS nrun_status_idx ON workflow.node_runs (tenant_id, status);
CREATE INDEX IF NOT EXISTS nrun_attempt_idx ON workflow.node_runs (run_id, attempt DESC);
CREATE TABLE IF NOT EXISTS workflow.timers (
    timer_id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id            UUID NOT NULL REFERENCES workflow.workflow_runs(run_id) ON DELETE CASCADE,
    node_run_id       UUID NOT NULL,
    tenant_id         VARCHAR(64) NOT NULL,
    fire_at           TIMESTAMPTZ NOT NULL,
    fired_at          TIMESTAMPTZ,
    callback_data     JSONB NOT NULL DEFAULT '{}'::jsonb,
    status            VARCHAR(32) NOT NULL,
    aggregate_version BIGINT NOT NULL DEFAULT 1,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS timer_due_idx ON workflow.timers (status, fire_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS timer_tenant_idx ON workflow.timers (tenant_id);
CREATE INDEX IF NOT EXISTS timer_firing_idx ON workflow.timers (updated_at) WHERE status = 'firing';
CREATE TABLE IF NOT EXISTS integration.schema_migrations (
    version     BIGINT PRIMARY KEY,
    description TEXT NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS integration.outbox (
    event_id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       VARCHAR(64) NOT NULL,
    domain          TEXT NOT NULL,
    operation       TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivering', 'delivered', 'failed')),
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    max_attempts    INTEGER NOT NULL DEFAULT 3,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, domain, operation, idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_integration_outbox_pending ON integration.outbox (next_attempt_at, attempt_count) WHERE status = 'pending' OR status = 'failed';
CREATE INDEX IF NOT EXISTS idx_integration_outbox_tenant ON integration.outbox (tenant_id, domain);
CREATE INDEX IF NOT EXISTS idx_integration_outbox_failed_updated ON integration.outbox (updated_at) WHERE status = 'failed';

CREATE TABLE IF NOT EXISTS integration.id_map (
    tenant_id       VARCHAR(64) NOT NULL,
    domain          TEXT NOT NULL,
    request_id      UUID NOT NULL,
    legacy_id       TEXT,
    external_id     TEXT,
    routing_version TEXT NOT NULL,
    normalized_hash TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    mapped_at       TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, domain, request_id)
);
CREATE INDEX IF NOT EXISTS idx_id_map_external ON integration.id_map (tenant_id, domain, external_id) WHERE external_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_id_map_legacy ON integration.id_map (tenant_id, domain, legacy_id) WHERE legacy_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS fencing.lease_ledger (
    lease_id      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     VARCHAR(64) NOT NULL,
    agent_id      VARCHAR(128) NOT NULL,
    issued_fence  BIGINT NOT NULL DEFAULT 0,
    revoked_fence BIGINT,
    state         TEXT NOT NULL CHECK (state IN ('issued','heartbeat','revoked','expired')),
    reason        TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS lease_ledger_tenant_agent_state_idx ON fencing.lease_ledger (tenant_id, agent_id, state, updated_at);

CREATE TABLE IF NOT EXISTS task_assigner_agents (
    agent_id      TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    skills        JSONB NOT NULL DEFAULT '[]'::jsonb,
    capabilities  JSONB NOT NULL DEFAULT '[]'::jsonb,
    status        TEXT NOT NULL DEFAULT 'online',
    workload      INT NOT NULL DEFAULT 0,
    max_workload  INT NOT NULL DEFAULT 5,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen     TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_ta_agents_status ON task_assigner_agents(status);

CREATE TABLE IF NOT EXISTS task_assigner_assignments (
    assignment_id         TEXT PRIMARY KEY,
    task_id               TEXT NOT NULL,
    task_info             JSONB NOT NULL DEFAULT '{}'::jsonb,
    required_skills       JSONB NOT NULL DEFAULT '[]'::jsonb,
    required_capabilities JSONB NOT NULL DEFAULT '[]'::jsonb,
    priority              TEXT NOT NULL DEFAULT 'medium',
    status                TEXT NOT NULL DEFAULT 'pending',
    agent_id              TEXT,
    assigned_at           TIMESTAMPTZ,
    started_at            TIMESTAMPTZ,
    completed_at          TIMESTAMPTZ,
    timeout               INT NOT NULL DEFAULT 30000,
    retry_count           INT NOT NULL DEFAULT 0,
    max_retries           INT NOT NULL DEFAULT 3,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    failed_reason         TEXT,
    paused_reason         TEXT,
    paused_at             TIMESTAMPTZ,
    result                JSONB NOT NULL DEFAULT '{}'::jsonb,
    rejection_count       INT NOT NULL DEFAULT 0,
    last_rejected_at      TIMESTAMPTZ,
    last_rejected_reason  TEXT
);
CREATE INDEX IF NOT EXISTS idx_ta_assign_status ON task_assigner_assignments(status);
CREATE INDEX IF NOT EXISTS idx_ta_assign_agent ON task_assigner_assignments(agent_id);

CREATE TABLE IF NOT EXISTS mcp_registry (
    name          VARCHAR(100) PRIMARY KEY,
    description   TEXT,
    base_url      VARCHAR(500) NOT NULL,
    status        VARCHAR(20) NOT NULL DEFAULT 'healthy' CHECK (status IN ('healthy','unhealthy','disabled')),
    capabilities  JSONB,
    allowed_roles TEXT[],
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_mcp_registry_status ON mcp_registry(status);

CREATE TABLE IF NOT EXISTS mcp_tools (
    id           BIGSERIAL PRIMARY KEY,
    server_name  VARCHAR(100) NOT NULL REFERENCES mcp_registry(name) ON DELETE CASCADE,
    tool_name    VARCHAR(200) NOT NULL,
    description  TEXT,
    input_schema JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (server_name, tool_name)
);
CREATE INDEX IF NOT EXISTS idx_mcp_tools_server ON mcp_tools(server_name);
CREATE INDEX IF NOT EXISTS idx_mcp_tools_name ON mcp_tools(tool_name);

INSERT INTO mcp_registry (name, description, base_url, status, allowed_roles) VALUES
    ('acc', 'Agent Control Center MCP', 'http://127.0.0.1:4100', 'healthy', NULL),
    ('memora', 'Memora / KxMemory MCP Server', 'http://127.0.0.1:3100', 'healthy', NULL),
    ('crm', 'CRM MCP Server', 'http://127.0.0.1:4200', 'healthy', ARRAY['admin','crm'])
ON CONFLICT (name) DO NOTHING;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA platform, workflow, integration, fencing TO platform_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON task_assigner_agents, task_assigner_assignments, mcp_registry, mcp_tools TO platform_app;
GRANT USAGE, SELECT ON SEQUENCE mcp_tools_id_seq TO platform_app;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'acc_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON task_assigner_agents, task_assigner_assignments, mcp_registry, mcp_tools TO acc_app;
        GRANT USAGE, SELECT ON SEQUENCE mcp_tools_id_seq TO acc_app;
    END IF;
END
$$;
INSERT INTO platform.schema_migrations (version, description) VALUES
    (1, 'local/shared platform core schema'),
    (24, 'local/shared platform_outbox retry columns')
ON CONFLICT (version) DO NOTHING;

INSERT INTO workflow.schema_migrations (version, description) VALUES
    (1, 'local/shared workflow schema'),
    (31, 'local/shared workflow timer claim columns')
ON CONFLICT (version) DO NOTHING;

INSERT INTO integration.schema_migrations (version, description) VALUES
    (1, 'local/shared integration outbox and id_map')
ON CONFLICT (version) DO NOTHING;

INSERT INTO public.schema_migrations (version, description)
VALUES ('641', 'local shared platform schema fixup for RedClaw/ACC observers')
ON CONFLICT (version) DO UPDATE
SET description = EXCLUDED.description,
    applied_at = NOW();

COMMIT;
