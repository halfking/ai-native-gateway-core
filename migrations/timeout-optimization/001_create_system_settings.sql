-- Migration: 001_create_system_settings
-- Purpose: 创建系统配置表，支持热更新
-- Date: 2026-07-22
-- Author: AI Agent

-- ============================================================================
-- 1. 创建 system_settings 表
-- ============================================================================

CREATE TABLE IF NOT EXISTS system_settings (
    id SERIAL PRIMARY KEY,
    key VARCHAR(255) NOT NULL UNIQUE,
    value JSONB NOT NULL,
    description TEXT,
    category VARCHAR(100) DEFAULT 'general',
    is_public BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    updated_by VARCHAR(100)
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_system_settings_key ON system_settings(key);
CREATE INDEX IF NOT EXISTS idx_system_settings_category ON system_settings(category);
CREATE INDEX IF NOT EXISTS idx_system_settings_updated ON system_settings(updated_at DESC);

-- 注释
COMMENT ON TABLE system_settings IS '系统配置表，支持热更新，配置值以JSONB格式存储';
COMMENT ON COLUMN system_settings.key IS '配置键，唯一标识';
COMMENT ON COLUMN system_settings.value IS '配置值，JSONB格式支持复杂结构';
COMMENT ON COLUMN system_settings.description IS '配置说明';
COMMENT ON COLUMN system_settings.category IS '配置分类：timeout/retry/continuation/general';
COMMENT ON COLUMN system_settings.is_public IS '是否为公开配置（可被前端访问）';
COMMENT ON COLUMN system_settings.updated_by IS '最后更新人（用户名或系统标识）';

-- ============================================================================
-- 2. 创建更新时间触发器
-- ============================================================================

CREATE OR REPLACE FUNCTION update_system_settings_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trigger_system_settings_updated_at ON system_settings;
CREATE TRIGGER trigger_system_settings_updated_at
    BEFORE UPDATE ON system_settings
    FOR EACH ROW
    EXECUTE FUNCTION update_system_settings_updated_at();

-- ============================================================================
-- 3. 插入默认配置
-- ============================================================================

-- 超时配置
INSERT INTO system_settings (key, value, description, category, is_public) VALUES
('timeout.client_default_seconds', '60', '客户端默认超时(秒)', 'timeout', false),
('timeout.upstream_base_seconds', '90', 'LLM节点基础超时(秒)', 'timeout', false),
('timeout.upstream_min_seconds', '20', 'LLM节点最小超时(秒)', 'timeout', false),
('timeout.upstream_max_seconds', '180', 'LLM节点最大超时(秒)', 'timeout', false),
('timeout.context_threshold_tokens', '20000', '上下文增加超时的阈值(tokens)', 'timeout', false),
('timeout.context_bonus_seconds', '45', '超过阈值时增加的超时(秒)', 'timeout', false),
('timeout.dynamic_mode', '"adaptive"', '动态调整模式: static/context_aware/network_aware/adaptive', 'timeout', false)
ON CONFLICT (key) DO NOTHING;

-- 重试配置
INSERT INTO system_settings (key, value, description, category, is_public) VALUES
('retry.max_attempts', '3', '最大重试次数(0-5)', 'retry', false),
('retry.base_delay_ms', '1000', '基础重试延迟(毫秒)', 'retry', false),
('retry.max_delay_ms', '10000', '最大重试延迟(毫秒)', 'retry', false),
('retry.exponential_backoff', 'true', '是否使用指数退避', 'retry', false),
('retry.keepalive_interval_seconds', '15', 'Keepalive发送间隔(秒)', 'retry', false),
('retry.last_node_wait_seconds', '10', '最后节点失败后等待时间(秒)', 'retry', false)
ON CONFLICT (key) DO NOTHING;

-- 继续/重试关键词配置
INSERT INTO system_settings (key, value, description, category, is_public) VALUES
('continuation.keywords', 
 '["继续", "请继续", "continue", "go on", "go", "重试", "请重试", "retry", "come on", "再来", "接着", "keep going", "carry on", "proceed"]',
 '继续/重试关键词列表(支持多语言)', 'continuation', false),
('continuation.cache_ttl_seconds', '3600', '响应缓存过期时间(秒)', 'continuation', false),
('continuation.max_cache_size_mb', '100', '最大缓存大小(MB)', 'continuation', false),
('continuation.enable_smart_detection', 'true', '是否启用智能检测（基于语义）', 'continuation', false)
ON CONFLICT (key) DO NOTHING;

-- 节点切换配置
INSERT INTO system_settings (key, value, description, category, is_public) VALUES
('node_switch.enable_notification', 'true', '是否发送节点切换通知', 'general', false),
('node_switch.max_switches_per_request', '3', '单次请求最大切换次数', 'general', false)
ON CONFLICT (key) DO NOTHING;

-- ============================================================================
-- 4. 创建配置查询辅助函数
-- ============================================================================

-- 获取配置值（自动解析JSONB）
CREATE OR REPLACE FUNCTION get_setting_value(setting_key VARCHAR)
RETURNS TEXT AS $$
DECLARE
    result TEXT;
BEGIN
    SELECT value #>> '{}' INTO result
    FROM system_settings
    WHERE key = setting_key;
    
    RETURN result;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION get_setting_value(VARCHAR) IS '获取配置值（返回文本格式）';

-- 获取整数配置
CREATE OR REPLACE FUNCTION get_setting_int(setting_key VARCHAR)
RETURNS INTEGER AS $$
DECLARE
    result INTEGER;
BEGIN
    SELECT (value #>> '{}')::INTEGER INTO result
    FROM system_settings
    WHERE key = setting_key;
    
    RETURN result;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION get_setting_int(VARCHAR) IS '获取整数配置值';

-- 获取布尔配置
CREATE OR REPLACE FUNCTION get_setting_bool(setting_key VARCHAR)
RETURNS BOOLEAN AS $$
DECLARE
    result BOOLEAN;
BEGIN
    SELECT (value #>> '{}')::BOOLEAN INTO result
    FROM system_settings
    WHERE key = setting_key;
    
    RETURN result;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION get_setting_bool(VARCHAR) IS '获取布尔配置值';

-- ============================================================================
-- 5. 验证
-- ============================================================================

-- 验证表创建成功
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'system_settings') THEN
        RAISE EXCEPTION 'Table system_settings was not created';
    END IF;
    
    RAISE NOTICE 'Migration 001_create_system_settings completed successfully';
END $$;

-- 显示插入的配置数量
SELECT 
    category,
    COUNT(*) as config_count
FROM system_settings
GROUP BY category
ORDER BY category;
