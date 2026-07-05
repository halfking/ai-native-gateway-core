-- Migration 023: settings_audit
-- 设置修改审计日志（Q6: C 保留 7 天，由 bg/settings_audit_cleaner.go 定时清理）
-- 作者: settings-management project
-- 日期: 2026-06-20

CREATE TABLE IF NOT EXISTS settings_audit (
    id              BIGSERIAL PRIMARY KEY,
    setting_key     VARCHAR(128) NOT NULL,
    tenant_id       VARCHAR(64),
    action          VARCHAR(16) NOT NULL,
    old_value       JSONB,
    new_value       JSONB,
    operator_user   VARCHAR(64) NOT NULL,
    operator_role   VARCHAR(32) NOT NULL,
    confirm_token   VARCHAR(64),
    client_ip       VARCHAR(45),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_settings_audit_key_time ON settings_audit (setting_key, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_settings_audit_tenant_time ON settings_audit (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_settings_audit_operator ON settings_audit (operator_user, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_settings_audit_created ON settings_audit (created_at);

COMMENT ON TABLE settings_audit IS '设置修改审计日志（bg/settings_audit_cleaner.go 每 24h 清理 7 天前的数据）';
COMMENT ON COLUMN settings_audit.action IS 'update / rollback / delete';