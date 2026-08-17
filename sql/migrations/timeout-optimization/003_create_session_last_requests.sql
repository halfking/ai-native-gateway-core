-- Migration: 003_create_session_last_requests
-- Purpose: 创建会话最后请求缓存表，支持继续/重试功能
-- Date: 2026-07-22
-- Author: AI Agent

-- ============================================================================
-- 1. 创建 session_last_requests 表
-- ============================================================================

CREATE TABLE IF NOT EXISTS session_last_requests (
    session_id VARCHAR(255) PRIMARY KEY,
    last_request_id BIGINT NOT NULL REFERENCES request_logs(id),
    last_request_status VARCHAR(50) NOT NULL,
    last_request_user_message TEXT,
    last_response_cached TEXT,
    last_response_chunks INT DEFAULT 0,
    last_model VARCHAR(100),
    last_provider_id INT,
    last_latency_ms INT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ DEFAULT NOW() + INTERVAL '1 hour'
);

-- ============================================================================
-- 2. 字段注释
-- ============================================================================

COMMENT ON TABLE session_last_requests IS '会话最后请求缓存表，用于支持继续/重试功能';
COMMENT ON COLUMN session_last_requests.session_id IS '会话ID，主键';
COMMENT ON COLUMN session_last_requests.last_request_id IS '最后一次请求的ID（外键到request_logs）';
COMMENT ON COLUMN session_last_requests.last_request_status IS '请求状态：success/timeout/error/client_disconnected';
COMMENT ON COLUMN session_last_requests.last_request_user_message IS '最后一次用户消息内容';
COMMENT ON COLUMN session_last_requests.last_response_cached IS '缓存的完整响应（客户端断开时保存）';
COMMENT ON COLUMN session_last_requests.last_response_chunks IS '响应chunk数量';
COMMENT ON COLUMN session_last_requests.last_model IS '使用的模型';
COMMENT ON COLUMN session_last_requests.last_provider_id IS '供应商ID';
COMMENT ON COLUMN session_last_requests.last_latency_ms IS '延迟(毫秒)';
COMMENT ON COLUMN session_last_requests.created_at IS '首次创建时间';
COMMENT ON COLUMN session_last_requests.updated_at IS '最后更新时间';
COMMENT ON COLUMN session_last_requests.expires_at IS '过期时间（默认1小时后）';

-- ============================================================================
-- 3. 创建索引
-- ============================================================================

-- 过期记录清理索引
CREATE INDEX IF NOT EXISTS idx_session_last_requests_expires 
ON session_last_requests(expires_at) 
WHERE expires_at IS NOT NULL;

-- 状态查询索引
CREATE INDEX IF NOT EXISTS idx_session_last_requests_status 
ON session_last_requests(last_request_status, updated_at DESC);

-- 模型查询索引
CREATE INDEX IF NOT EXISTS idx_session_last_requests_model 
ON session_last_requests(last_model, updated_at DESC);

-- ============================================================================
-- 4. 创建更新时间触发器
-- ============================================================================

CREATE OR REPLACE FUNCTION update_session_last_requests_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trigger_session_last_requests_updated_at ON session_last_requests;
CREATE TRIGGER trigger_session_last_requests_updated_at
    BEFORE UPDATE ON session_last_requests
    FOR EACH ROW
    EXECUTE FUNCTION update_session_last_requests_updated_at();

-- ============================================================================
-- 5. 创建辅助函数
-- ============================================================================

-- 更新或插入会话最后请求
CREATE OR REPLACE FUNCTION upsert_session_last_request(
    p_session_id VARCHAR,
    p_request_id BIGINT,
    p_status VARCHAR,
    p_user_message TEXT,
    p_response_cached TEXT,
    p_response_chunks INT,
    p_model VARCHAR,
    p_provider_id INT,
    p_latency_ms INT,
    p_ttl_seconds INT DEFAULT 3600
)
RETURNS VOID AS $$
BEGIN
    INSERT INTO session_last_requests (
        session_id,
        last_request_id,
        last_request_status,
        last_request_user_message,
        last_response_cached,
        last_response_chunks,
        last_model,
        last_provider_id,
        last_latency_ms,
        expires_at
    ) VALUES (
        p_session_id,
        p_request_id,
        p_status,
        p_user_message,
        p_response_cached,
        p_response_chunks,
        p_model,
        p_provider_id,
        p_latency_ms,
        NOW() + (p_ttl_seconds || ' seconds')::INTERVAL
    )
    ON CONFLICT (session_id) 
    DO UPDATE SET
        last_request_id = EXCLUDED.last_request_id,
        last_request_status = EXCLUDED.last_request_status,
        last_request_user_message = EXCLUDED.last_request_user_message,
        last_response_cached = EXCLUDED.last_response_cached,
        last_response_chunks = EXCLUDED.last_response_chunks,
        last_model = EXCLUDED.last_model,
        last_provider_id = EXCLUDED.last_provider_id,
        last_latency_ms = EXCLUDED.last_latency_ms,
        expires_at = EXCLUDED.expires_at;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION upsert_session_last_request IS '更新或插入会话最后请求记录';

-- 获取会话最后请求信息
CREATE OR REPLACE FUNCTION get_session_last_request(p_session_id VARCHAR)
RETURNS TABLE (
    request_id BIGINT,
    status VARCHAR,
    user_message TEXT,
    cached_response TEXT,
    response_chunks INT,
    model VARCHAR,
    provider_id INT,
    latency_ms INT,
    age_seconds INT
) AS $$
BEGIN
    RETURN QUERY
    SELECT 
        last_request_id,
        last_request_status,
        last_request_user_message,
        last_response_cached,
        last_response_chunks,
        last_model,
        last_provider_id,
        last_latency_ms,
        EXTRACT(EPOCH FROM (NOW() - updated_at))::INT as age_seconds
    FROM session_last_requests
    WHERE session_id = p_session_id
      AND expires_at > NOW();
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION get_session_last_request IS '获取会话最后请求信息（未过期）';

-- 清理过期记录
CREATE OR REPLACE FUNCTION cleanup_expired_session_requests()
RETURNS TABLE (deleted_count BIGINT) AS $$
DECLARE
    result BIGINT;
BEGIN
    DELETE FROM session_last_requests
    WHERE expires_at <= NOW();
    
    GET DIAGNOSTICS result = ROW_COUNT;
    
    RETURN QUERY SELECT result;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION cleanup_expired_session_requests IS '清理过期的会话请求缓存';

-- ============================================================================
-- 6. 创建统计视图
-- ============================================================================

-- 缓存状态统计视图
CREATE OR REPLACE VIEW v_session_cache_stats AS
SELECT 
    last_request_status as status,
    COUNT(*) as session_count,
    COUNT(*) FILTER (WHERE last_response_cached IS NOT NULL) as cached_count,
    ROUND(AVG(last_response_chunks), 2) as avg_chunks,
    ROUND(AVG(last_latency_ms)/1000.0, 2) as avg_latency_seconds,
    ROUND(AVG(EXTRACT(EPOCH FROM (NOW() - updated_at))/60.0), 2) as avg_age_minutes
FROM session_last_requests
WHERE expires_at > NOW()
GROUP BY last_request_status
ORDER BY session_count DESC;

COMMENT ON VIEW v_session_cache_stats IS '会话缓存状态统计';

-- 模型级别缓存统计
CREATE OR REPLACE VIEW v_session_cache_by_model AS
SELECT 
    last_model as model,
    COUNT(*) as session_count,
    COUNT(*) FILTER (WHERE last_response_cached IS NOT NULL) as cached_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE last_response_cached IS NOT NULL) / COUNT(*), 2) as cache_rate,
    ROUND(AVG(last_latency_ms)/1000.0, 2) as avg_latency_seconds
FROM session_last_requests
WHERE expires_at > NOW()
  AND last_model IS NOT NULL
GROUP BY last_model
ORDER BY session_count DESC;

COMMENT ON VIEW v_session_cache_by_model IS '按模型统计缓存情况';

-- ============================================================================
-- 7. 创建定时清理任务配置
-- ============================================================================

-- 插入清理任务配置到 system_settings
INSERT INTO system_settings (key, value, description, category) VALUES
('session_cache.cleanup_interval_seconds', '300', '会话缓存清理间隔(秒)，默认5分钟', 'continuation'),
('session_cache.default_ttl_seconds', '3600', '会话缓存默认过期时间(秒)，默认1小时', 'continuation')
ON CONFLICT (key) DO NOTHING;

-- ============================================================================
-- 8. 创建示例数据（仅测试环境）
-- ============================================================================

-- 注释掉，生产环境不需要示例数据
/*
INSERT INTO session_last_requests (
    session_id,
    last_request_id,
    last_request_status,
    last_request_user_message,
    last_response_cached,
    last_response_chunks,
    last_model,
    expires_at
) VALUES 
(
    'test_session_1',
    1,
    'client_disconnected',
    '请帮我生成一个Python函数',
    '这是缓存的完整响应...',
    15,
    'minimax-m3',
    NOW() + INTERVAL '1 hour'
);
*/

-- ============================================================================
-- 9. 验证
-- ============================================================================

DO $$
DECLARE
    table_exists BOOLEAN;
    index_count INT;
    function_count INT;
BEGIN
    -- 验证表是否存在
    SELECT EXISTS (
        SELECT 1 FROM pg_tables WHERE tablename = 'session_last_requests'
    ) INTO table_exists;
    
    IF NOT table_exists THEN
        RAISE EXCEPTION 'Table session_last_requests was not created';
    END IF;
    
    -- 验证索引数量
    SELECT COUNT(*) INTO index_count
    FROM pg_indexes
    WHERE tablename = 'session_last_requests';
    
    -- 验证函数数量
    SELECT COUNT(*) INTO function_count
    FROM pg_proc
    WHERE proname IN (
        'upsert_session_last_request',
        'get_session_last_request',
        'cleanup_expired_session_requests'
    );
    
    RAISE NOTICE 'Migration 003_create_session_last_requests completed successfully';
    RAISE NOTICE 'Created % indexes and % functions', index_count, function_count;
END $$;

-- 显示表结构
\d session_last_requests

-- 显示视图
SELECT 
    table_name as view_name,
    view_definition
FROM information_schema.views
WHERE table_name LIKE 'v_session_cache%'
ORDER BY table_name;
