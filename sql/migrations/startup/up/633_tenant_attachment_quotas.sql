-- Migration 633: Tenant Attachment Quotas
-- P0 安全修复 (2026-11-26): 防止恶意租户通过无限制上传耗尽存储空间
--
-- 功能：
-- 1. 追踪每租户的附件存储使用量（字节）
-- 2. 追踪每租户的每日上传文件数
-- 3. 自动每日重置文件计数器
--
-- 使用方：domains/attachments/quota.go (QuotaManager)

CREATE TABLE IF NOT EXISTS tenant_attachment_quotas (
    tenant_id TEXT PRIMARY KEY,
    current_bytes BIGINT NOT NULL DEFAULT 0,
    files_today INTEGER NOT NULL DEFAULT 0,
    last_reset_date DATE NOT NULL DEFAULT CURRENT_DATE,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- 索引：按更新时间查询（监控、清理）
CREATE INDEX IF NOT EXISTS idx_tenant_attachment_quotas_updated_at 
    ON tenant_attachment_quotas(updated_at);

-- 注释
COMMENT ON TABLE tenant_attachment_quotas IS 
    'P0 security fix: Per-tenant attachment storage quotas to prevent DoS via unlimited uploads';
COMMENT ON COLUMN tenant_attachment_quotas.tenant_id IS 
    'Tenant identifier (from auth.keyInfo.TenantID)';
COMMENT ON COLUMN tenant_attachment_quotas.current_bytes IS 
    'Total attachment storage used by this tenant (bytes)';
COMMENT ON COLUMN tenant_attachment_quotas.files_today IS 
    'Number of files uploaded today (resets daily)';
COMMENT ON COLUMN tenant_attachment_quotas.last_reset_date IS 
    'Date when files_today was last reset (for daily limit enforcement)';
COMMENT ON COLUMN tenant_attachment_quotas.updated_at IS 
    'Last update timestamp (for monitoring and cleanup)';
