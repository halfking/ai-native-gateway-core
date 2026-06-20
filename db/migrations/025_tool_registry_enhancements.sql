-- Migration 025: Tool Registry Enhancements
-- Phase 3.2/3.3/3.4: Version management, usage stats, permission control
-- Date: 2026-06-20

-- ============================================================================
-- Part 1: 工具版本管理 (Phase 3.2)
-- ============================================================================

-- 添加版本相关字段
ALTER TABLE tool_registry
    ADD COLUMN IF NOT EXISTS deprecation_date TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS min_client_version VARCHAR(32),
    ADD COLUMN IF NOT EXISTS breaking_changes JSONB DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS superseded_by VARCHAR(128);

-- 添加版本索引（加速版本查询）
CREATE INDEX IF NOT EXISTS idx_tool_registry_deprecation 
    ON tool_registry(deprecation_date) 
    WHERE deprecation_date IS NOT NULL;

-- 添加注释
COMMENT ON COLUMN tool_registry.deprecation_date IS 
    'Deprecated after this date (Phase 3.2: 版本管理)';
COMMENT ON COLUMN tool_registry.min_client_version IS 
    'Minimum client version required (Phase 3.2: 版本管理)';
COMMENT ON COLUMN tool_registry.breaking_changes IS 
    'List of breaking changes in this version (Phase 3.2: 版本管理)';
COMMENT ON COLUMN tool_registry.superseded_by IS 
    'Newer tool_id that replaces this version (Phase 3.2: 版本管理)';

-- ============================================================================
-- Part 2: 工具使用统计 (Phase 3.3)
-- ============================================================================

-- 创建工具使用统计表
CREATE TABLE IF NOT EXISTS tool_usage_stats (
    id BIGSERIAL PRIMARY KEY,
    tool_id VARCHAR(128) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL DEFAULT 'default',
    usage_date DATE NOT NULL DEFAULT CURRENT_DATE,
    call_count BIGINT NOT NULL DEFAULT 0,
    success_count BIGINT NOT NULL DEFAULT 0,
    error_count BIGINT NOT NULL DEFAULT 0,
    avg_latency_ms INT DEFAULT 0,
    last_called_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 唯一约束：每天每个工具每个租户只有一条记录
    CONSTRAINT uk_tool_usage_stats UNIQUE (tool_id, tenant_id, usage_date)
);

-- 创建索引
CREATE INDEX IF NOT EXISTS idx_tool_usage_stats_tool_id 
    ON tool_usage_stats(tool_id);
CREATE INDEX IF NOT EXISTS idx_tool_usage_stats_tenant_id 
    ON tool_usage_stats(tenant_id);
CREATE INDEX IF NOT EXISTS idx_tool_usage_stats_date 
    ON tool_usage_stats(usage_date DESC);
CREATE INDEX IF NOT EXISTS idx_tool_usage_stats_tool_tenant 
    ON tool_usage_stats(tool_id, tenant_id, usage_date DESC);

-- 添加注释
COMMENT ON TABLE tool_usage_stats IS 
    'Tool usage statistics (Phase 3.3: 使用统计)';
COMMENT ON COLUMN tool_usage_stats.call_count IS 
    'Total call count for this tool on this day';
COMMENT ON COLUMN tool_usage_stats.success_count IS 
    'Successful call count';
COMMENT ON COLUMN tool_usage_stats.error_count IS 
    'Failed call count';

-- 创建工具调用事件表（用于详细分析）
CREATE TABLE IF NOT EXISTS tool_call_events (
    id BIGSERIAL PRIMARY KEY,
    tool_id VARCHAR(128) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL DEFAULT 'default',
    request_id VARCHAR(64),
    api_key VARCHAR(64),
    status VARCHAR(16) NOT NULL,
    latency_ms INT DEFAULT 0,
    error_code VARCHAR(64),
    called_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    CONSTRAINT chk_status CHECK (status IN ('success', 'error', 'timeout'))
);

-- 创建索引（用于查询）
CREATE INDEX IF NOT EXISTS idx_tool_call_events_tool_id 
    ON tool_call_events(tool_id, called_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_call_events_tenant_id 
    ON tool_call_events(tenant_id, called_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_call_events_called_at 
    ON tool_call_events(called_at DESC);

-- 添加注释
COMMENT ON TABLE tool_call_events IS 
    'Individual tool call events (Phase 3.3: 详细调用记录)';

-- ============================================================================
-- Part 3: 工具权限控制 (Phase 3.4)
-- ============================================================================

-- 创建租户工具策略表
CREATE TABLE IF NOT EXISTS tenant_tool_policies (
    id BIGSERIAL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    tool_pattern VARCHAR(128) NOT NULL,
    policy_type VARCHAR(16) NOT NULL,
    reason VARCHAR(256),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by VARCHAR(128),
    
    -- 约束：policy_type 必须是 allow 或 deny
    CONSTRAINT chk_policy_type CHECK (policy_type IN ('allow', 'deny')),
    
    -- 唯一约束
    CONSTRAINT uk_tenant_tool_policy 
        UNIQUE (tenant_id, tool_pattern)
);

-- 创建索引
CREATE INDEX IF NOT EXISTS idx_tenant_tool_policies_tenant 
    ON tenant_tool_policies(tenant_id) WHERE enabled = TRUE;
CREATE INDEX IF NOT EXISTS idx_tenant_tool_policies_enabled 
    ON tenant_tool_policies(enabled);

-- 添加注释
COMMENT ON TABLE tenant_tool_policies IS 
    'Tenant-level tool access policies (Phase 3.4: 权限控制)';
COMMENT ON COLUMN tenant_tool_policies.tool_pattern IS 
    'Tool pattern: exact match (filesystem.read_file) or wildcard (filesystem.*)';
COMMENT ON COLUMN tenant_tool_policies.policy_type IS 
    'Policy type: allow (whitelist) or deny (blacklist)';
COMMENT ON COLUMN tenant_tool_policies.reason IS 
    'Reason for this policy (audit trail)';

-- ============================================================================
-- Part 4: 更新版本字段约束（已有 version 字段，确保默认值为 1）
-- ============================================================================

-- 确保 version 字段有默认值
ALTER TABLE tool_registry
    ALTER COLUMN version SET DEFAULT 1;

-- 添加 version 字段注释（如果还没有）
COMMENT ON COLUMN tool_registry.version IS
    'Tool version (Phase 3.2: 多版本共存)';