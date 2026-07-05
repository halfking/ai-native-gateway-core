-- Phase 3: Tool Registry Extensions - Tenant Isolation and Tool IDs
-- Migration: 028_tool_registry_extensions.sql
-- Date: 2026-06-21

BEGIN;

-- 1. 添加新字段
ALTER TABLE tool_registry
    ADD COLUMN IF NOT EXISTS tool_id VARCHAR(128),           -- "filesystem.read_file"
    ADD COLUMN IF NOT EXISTS tenant_id VARCHAR(64) DEFAULT 'default',
    ADD COLUMN IF NOT EXISTS version INT DEFAULT 1;

-- 2. 更新现有数据（为已有记录生成 tool_id）
-- tool_id = category + '.' + snake_case(tool_name)
UPDATE tool_registry
SET tool_id = category || '.' || tool_name
WHERE tool_id IS NULL;

-- 3. 添加 NOT NULL 约束
ALTER TABLE tool_registry
    ALTER COLUMN tool_id SET NOT NULL;

-- 4. 创建索引（支持租户覆盖查询）
-- 优先级：tenant_id, tool_id, version DESC
CREATE INDEX IF NOT EXISTS idx_tool_registry_tenant_tool 
    ON tool_registry(tenant_id, tool_id, version DESC);

-- 5. 创建唯一约束（确保未来多版本扩展）
-- 同一租户下，同一 tool_id 只能有一个版本
CREATE UNIQUE INDEX IF NOT EXISTS idx_tool_registry_unique_version
    ON tool_registry(tenant_id, tool_id, version);

-- 6. 添加注释
COMMENT ON COLUMN tool_registry.tool_id IS 'Phase 3: Unique tool identifier (category.tool_name)';
COMMENT ON COLUMN tool_registry.tenant_id IS 'Phase 3: Tenant isolation (default = global shared)';
COMMENT ON COLUMN tool_registry.version IS 'Phase 3: Tool version (fixed at 1 for Phase 3.0)';

COMMIT;
