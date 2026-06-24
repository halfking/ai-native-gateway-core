-- 050_agents.sql — Agent Registry (Q1 2027 D1-1)
--
-- 注册所有可被网关调用的 Agent (OpenClaw / brandmind-go / crm-go / 自定义).
-- 与 apihub.assets(kind=agent) 互为补充: assets 存"统一身份 + 健康状态",
-- agents 表存"agent 特有元数据" (kind + capabilities JSONB + endpoint).
--
-- 关系: A0 已建 assets(kind, ref_id) 复合主键. agents 表的 id 与
-- assets(kind=agent).ref_id 一一对应, 由后台 watcher (复用 A1 watcher 模式)
-- 自动同步 (D1-6 留作 follow-on).
--
-- RLS: 租户隔离 (同 assets 表约定, get_current_tenant).
-- Idempotent: safe to re-run (DROP ... IF EXISTS guards).

BEGIN;

CREATE TABLE IF NOT EXISTS public.agents (
    id              bigserial    PRIMARY KEY,
    tenant_id       text         NOT NULL,
    name            text         NOT NULL,
    kind            text         NOT NULL,
    endpoint        text         NOT NULL,           -- e.g. "https://brandmind.kxpms.cn/api/v1/agent"
    status          text         NOT NULL DEFAULT 'unknown',  -- 'healthy' | 'degraded' | 'down' | 'unknown'
    capabilities    jsonb        NOT NULL DEFAULT '{}'::jsonb,
    version         text         NOT NULL DEFAULT '0.0.0',
    auth_scheme     text         ,                     -- 'bearer' | 'api_key' | 'mtls' | 'none' (A2A spec 对齐)
    last_heartbeat  timestamptz  ,
    registered_at   timestamptz  NOT NULL DEFAULT now(),
    updated_at      timestamptz  NOT NULL DEFAULT now(),
    metadata        jsonb        NOT NULL DEFAULT '{}'::jsonb,

    CONSTRAINT chk_agents_kind CHECK (kind IN (
        'openclaw',         -- OpenClaw 框架
        'brandmind-go',     -- brandmind-go 服务
        'crm-go',           -- crm-go 服务
        'custom'            -- 用户自建 / 第三方 (含 A2A 兼容)
    )),
    CONSTRAINT chk_agents_status CHECK (status IN (
        'healthy', 'degraded', 'down', 'unknown'
    )),
    CONSTRAINT chk_agents_auth  CHECK (auth_scheme IS NULL OR auth_scheme IN (
        'bearer', 'api_key', 'mtls', 'none'
    ))
);

-- Tenant-scoped lookups (hot path: list a tenant's agents).
CREATE INDEX IF NOT EXISTS idx_agents_tenant
    ON public.agents (tenant_id);

-- Discovery by kind (e.g., list all openclaw agents across the gateway).
CREATE INDEX IF NOT EXISTS idx_agents_kind
    ON public.agents (tenant_id, kind);

-- Heartbeat-based health (bg worker SELECTs WHERE last_heartbeat < threshold).
CREATE INDEX IF NOT EXISTS idx_agents_heartbeat
    ON public.agents (last_heartbeat)
    WHERE last_heartbeat IS NOT NULL;

-- Capabilities GIN index (e.g., "find agents with capability=function_calling").
CREATE INDEX IF NOT EXISTS idx_agents_capabilities
    ON public.agents USING gin (capabilities jsonb_path_ops);

-- RLS: 租户隔离
ALTER TABLE public.agents ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_agents ON public.agents;
CREATE POLICY tenant_isolation_agents ON public.agents
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

COMMIT;