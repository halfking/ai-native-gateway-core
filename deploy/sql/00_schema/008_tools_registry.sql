-- ============================================================================
-- llm-gateway-go 工具注册表和其他辅助表结构
-- ============================================================================

-- ----------------------------------------------------------------------------
-- Tool Registry 表 - 工具注册表
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tool_registry (
    id SERIAL PRIMARY KEY,
    tool_name TEXT NOT NULL,
    tenant_id TEXT,
    display_name TEXT NOT NULL,
    description TEXT,
    category TEXT,
    input_schema JSONB NOT NULL,
    output_schema JSONB,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    metadata JSONB,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_tool_registry_name ON tool_registry(tool_name);
CREATE INDEX IF NOT EXISTS idx_tool_registry_category ON tool_registry(category);
CREATE INDEX IF NOT EXISTS idx_tool_registry_tenant ON tool_registry(tenant_id);

-- RLS
ALTER TABLE public.tool_registry ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tool_registry ON public.tool_registry;
CREATE POLICY tenant_isolation_tool_registry ON public.tool_registry
    USING ((tenant_id)::text = (public.get_current_tenant())::text
           OR (tenant_id) IS NULL OR (tenant_id) = 'default');

-- ----------------------------------------------------------------------------
-- Tool Categories 表 - 工具分类
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tool_categories (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    description TEXT,
    parent_id INT REFERENCES tool_categories(id) ON DELETE SET NULL,
    sort_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ----------------------------------------------------------------------------
-- Tenant Tool Policies 表 - 租户工具策略
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tenant_tool_policies (
    id SERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    action VARCHAR(32) NOT NULL DEFAULT 'allow'
        CHECK (action IN ('allow', 'deny', 'audit')),
    reason TEXT,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(tenant_id, tool_name)
);
CREATE INDEX IF NOT EXISTS idx_tenant_tool_policies_tenant ON tenant_tool_policies(tenant_id);

-- RLS
ALTER TABLE public.tenant_tool_policies ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tenant_tool_policies ON public.tenant_tool_policies;
CREATE POLICY tenant_isolation_tenant_tool_policies ON public.tenant_tool_policies
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- ----------------------------------------------------------------------------
-- Tool Call Events 表 - 工具调用事件
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tool_call_events (
    id BIGSERIAL PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id TEXT NOT NULL,
    api_key_id BIGINT,
    request_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    input_tokens INT,
    output_tokens INT,
    total_tokens INT,
    latency_ms INT,
    status VARCHAR(32),
    error_message TEXT,
    metadata JSONB
);
CREATE INDEX IF NOT EXISTS idx_tool_call_events_tenant_ts ON tool_call_events(tenant_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_tool_call_events_tool ON tool_call_events(tool_name);

-- RLS
ALTER TABLE public.tool_call_events ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tool_call_events ON public.tool_call_events;
CREATE POLICY tenant_isolation_tool_call_events ON public.tool_call_events
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- ----------------------------------------------------------------------------
-- Tool Usage Stats 表（分区表）- 工具使用统计
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tool_usage_stats (
    id BIGSERIAL,
    tenant_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    stat_date DATE NOT NULL,
    total_calls BIGINT NOT NULL DEFAULT 0,
    total_input_tokens BIGINT NOT NULL DEFAULT 0,
    total_output_tokens BIGINT NOT NULL DEFAULT 0,
    avg_latency_ms NUMERIC(10,2),
    error_count BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id, stat_date)
) PARTITION BY RANGE (stat_date);

CREATE TABLE IF NOT EXISTS public.tool_usage_stats_2026_06
    PARTITION OF public.tool_usage_stats
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

CREATE TABLE IF NOT EXISTS public.tool_usage_stats_2026_07
    PARTITION OF public.tool_usage_stats
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

CREATE TABLE IF NOT EXISTS public.tool_usage_stats_2026_08
    PARTITION OF public.tool_usage_stats
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');

CREATE TABLE IF NOT EXISTS public.tool_usage_stats_old (
    LIKE public.tool_usage_stats INCLUDING ALL
);

-- RLS
ALTER TABLE public.tool_usage_stats ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tool_usage_stats ON public.tool_usage_stats;
CREATE POLICY tenant_isolation_tool_usage_stats ON public.tool_usage_stats
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- ----------------------------------------------------------------------------
-- Tenant Model Policies 表 - 租户模型策略
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tenant_model_policies (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL REFERENCES tenants(code) ON DELETE CASCADE,
    canonical_name TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_by VARCHAR(128) NOT NULL DEFAULT '',
    deleted_at TIMESTAMPTZ,
    deleted_by VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, canonical_name),
    CHECK (canonical_name <> '')
);
CREATE INDEX IF NOT EXISTS idx_tmp_tenant_active ON tenant_model_policies (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_tmp_canonical ON tenant_model_policies (canonical_name);

-- RLS
ALTER TABLE public.tenant_model_policies ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tmp ON public.tenant_model_policies;
CREATE POLICY tenant_isolation_tmp ON public.tenant_model_policies
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- 创建活跃视图
CREATE OR REPLACE VIEW tenant_model_policies_active AS
    SELECT id, tenant_id, canonical_name, reason, created_by,
           created_at, updated_at
    FROM tenant_model_policies
    WHERE deleted_at IS NULL;

-- ----------------------------------------------------------------------------
-- Tenant Model Policies Audit 表 - 模型策略审计
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tenant_model_policies_audit (
    id BIGSERIAL PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL DEFAULT now(),
    action TEXT NOT NULL CHECK (action IN ('insert', 'update', 'delete', 'undelete')),
    policy_id BIGINT,
    tenant_id TEXT,
    canonical_name TEXT,
    reason TEXT,
    actor TEXT
);
CREATE INDEX IF NOT EXISTS idx_tmp_audit_ts ON tenant_model_policies_audit (ts DESC);
CREATE INDEX IF NOT EXISTS idx_tmp_audit_tenant_ts ON tenant_model_policies_audit (tenant_id, ts DESC);

-- RLS
ALTER TABLE public.tenant_model_policies_audit ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tmp_audit ON public.tenant_model_policies_audit;
CREATE POLICY tenant_isolation_tmp_audit ON public.tenant_model_policies_audit
    USING ((tenant_id)::text = (public.get_current_tenant())::text
           OR (tenant_id) IS NULL);

-- ----------------------------------------------------------------------------
-- Settings KV 表 - 系统设置键值对
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.settings_kv (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ----------------------------------------------------------------------------
-- Tenant Settings KV 表 - 租户设置键值对
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.tenant_settings_kv (
    id BIGSERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(tenant_id, key)
);
CREATE INDEX IF NOT EXISTS idx_tenant_settings_kv_tenant ON tenant_settings_kv(tenant_id);

-- RLS
ALTER TABLE public.tenant_settings_kv ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_tenant_settings_kv ON public.tenant_settings_kv;
CREATE POLICY tenant_isolation_tenant_settings_kv ON public.tenant_settings_kv
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- ----------------------------------------------------------------------------
-- Settings Audit 表 - 设置变更审计
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.settings_audit (
    id BIGSERIAL PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id TEXT,
    key TEXT NOT NULL,
    action TEXT NOT NULL,
    old_value JSONB,
    new_value JSONB,
    actor TEXT
);
CREATE INDEX IF NOT EXISTS idx_settings_audit_ts ON settings_audit(ts DESC);

-- RLS
ALTER TABLE public.settings_audit ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_settings_audit ON public.settings_audit;
CREATE POLICY tenant_isolation_settings_audit ON public.settings_audit
    USING ((tenant_id)::text = (public.get_current_tenant())::text
           OR (tenant_id) IS NULL);

-- ----------------------------------------------------------------------------
-- Session Titles 表 - 会话标题
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.session_titles (
    task_id TEXT NOT NULL,
    scoped_session_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL,
    generated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    model TEXT,
    api_key_id INT,
    PRIMARY KEY (task_id, scoped_session_id)
);
CREATE INDEX IF NOT EXISTS idx_session_titles_generated_at ON session_titles(generated_at DESC);

-- ----------------------------------------------------------------------------
-- Session Memora Extraction Log 表 - 会话记忆提取日志
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.session_memora_extraction_log (
    task_id TEXT PRIMARY KEY,
    extracted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    written INT NOT NULL DEFAULT 0,
    skipped_noise INT NOT NULL DEFAULT 0,
    skipped_duplicate INT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'ok',
    detail JSONB
);
CREATE INDEX IF NOT EXISTS idx_session_memora_extraction_at ON session_memora_extraction_log(extracted_at DESC);

-- ----------------------------------------------------------------------------
-- API Key Auto Profile 表 - API密钥自动配置
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.api_key_auto_profile (
    id SERIAL PRIMARY KEY,
    api_key_id BIGINT NOT NULL,
    auto_profile TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ----------------------------------------------------------------------------
-- API Key Model Cost 表 - API密钥模型成本
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.api_key_model_cost (
    id SERIAL PRIMARY KEY,
    api_key_id BIGINT NOT NULL,
    model_name TEXT NOT NULL,
    call_count BIGINT NOT NULL DEFAULT 0,
    total_tokens BIGINT NOT NULL DEFAULT 0,
    last_call_at TIMESTAMPTZ,
    UNIQUE(api_key_id, model_name)
);
CREATE INDEX IF NOT EXISTS idx_api_key_model_cost_key ON api_key_model_cost(api_key_id);

-- ----------------------------------------------------------------------------
-- Model Offers Legacy 表 - 旧版模型报价
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_offers_legacy (
    id SERIAL PRIMARY KEY,
    provider_id INT,
    model_name TEXT NOT NULL,
    offer_type VARCHAR(32) NOT NULL,
    price_per_1m_input NUMERIC(10,2),
    price_per_1m_output NUMERIC(10,2),
    valid_from TIMESTAMPTZ,
    valid_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ----------------------------------------------------------------------------
-- Sticky Sessions 表 - 粘性会话
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.sticky_sessions (
    session_id TEXT PRIMARY KEY,
    credential_id INT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_sticky_sessions_expires ON sticky_sessions(expires_at);