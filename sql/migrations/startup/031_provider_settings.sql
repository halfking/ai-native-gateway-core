-- Migration 025: Provider-level settings override
-- Date: 2026-06-20
-- Purpose: Support provider-level configuration for compression, cache, etc.

-- Provider级别的配置覆盖表
CREATE TABLE IF NOT EXISTS provider_settings (
    id BIGSERIAL PRIMARY KEY,
    provider_id BIGINT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    setting_key TEXT NOT NULL,
    setting_value JSONB NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_by TEXT DEFAULT 'system',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT provider_settings_unique_key UNIQUE(provider_id, setting_key)
);

CREATE INDEX idx_provider_settings_provider ON provider_settings(provider_id) WHERE enabled = TRUE;
CREATE INDEX idx_provider_settings_key ON provider_settings(setting_key) WHERE enabled = TRUE;

-- 触发器：自动更新updated_at
CREATE OR REPLACE FUNCTION update_provider_settings_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trigger_provider_settings_updated_at
    BEFORE UPDATE ON provider_settings
    FOR EACH ROW
    EXECUTE FUNCTION update_provider_settings_updated_at();

-- 注释说明
COMMENT ON TABLE provider_settings IS 'Provider级别的配置覆盖，优先级高于平台默认配置';
COMMENT ON COLUMN provider_settings.setting_key IS '配置键，如: compression.mode, cache.enabled, format_conversion.enabled';
COMMENT ON COLUMN provider_settings.setting_value IS '配置值，JSON格式，如: "off", true, false';
COMMENT ON COLUMN provider_settings.enabled IS '是否启用该配置覆盖';

-- 支持的配置项（文档用）:
-- compression.mode: "off" | "auto_threshold" | "on_4xx" | null (null表示跟随全局)
-- cache.enabled: boolean (是否启用会话缓存)
-- format_conversion.enabled: boolean (是否启用Anthropic↔OpenAI格式转换)
