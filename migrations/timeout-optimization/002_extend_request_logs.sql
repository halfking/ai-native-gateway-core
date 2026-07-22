-- Migration: 002_extend_request_logs
-- Purpose: 扩展request_logs表，支持超时优化和缓存功能
-- Date: 2026-07-22
-- Author: AI Agent

-- ============================================================================
-- 1. 扩展 request_logs 表字段
-- ============================================================================

-- 添加超时相关字段
ALTER TABLE request_logs 
ADD COLUMN IF NOT EXISTS effective_timeout_seconds INT;

ALTER TABLE request_logs 
ADD COLUMN IF NOT EXISTS context_size_tokens INT;

ALTER TABLE request_logs 
ADD COLUMN IF NOT EXISTS timeout_mode VARCHAR(50);

-- 添加继续/重试相关字段
ALTER TABLE request_logs 
ADD COLUMN IF NOT EXISTS is_continuation BOOLEAN DEFAULT FALSE;

ALTER TABLE request_logs 
ADD COLUMN IF NOT EXISTS continuation_keywords TEXT[];

ALTER TABLE request_logs 
ADD COLUMN IF NOT EXISTS cached_response_id BIGINT REFERENCES request_logs(id);

-- 添加节点切换相关字段
ALTER TABLE request_logs 
ADD COLUMN IF NOT EXISTS node_switch_count INT DEFAULT 0;

ALTER TABLE request_logs 
ADD COLUMN IF NOT EXISTS keepalive_sent_count INT DEFAULT 0;

-- ============================================================================
-- 2. 字段注释
-- ============================================================================

COMMENT ON COLUMN request_logs.effective_timeout_seconds IS '实际使用的超时时间(秒)，动态计算后的值';
COMMENT ON COLUMN request_logs.context_size_tokens IS '请求上下文大小(tokens)，用于动态超时计算';
COMMENT ON COLUMN request_logs.timeout_mode IS '超时模式：static/context_aware/network_aware/adaptive';
COMMENT ON COLUMN request_logs.is_continuation IS '是否为继续/重试请求';
COMMENT ON COLUMN request_logs.continuation_keywords IS '检测到的继续/重试关键词列表';
COMMENT ON COLUMN request_logs.cached_response_id IS '如果使用缓存，指向原始请求的ID';
COMMENT ON COLUMN request_logs.node_switch_count IS '节点切换次数';
COMMENT ON COLUMN request_logs.keepalive_sent_count IS 'Keepalive消息发送次数';

-- ============================================================================
-- 3. 创建索引
-- ============================================================================

-- 继续/重试查询优化
CREATE INDEX IF NOT EXISTS idx_request_logs_continuation 
ON request_logs(session_id, created_at DESC) 
WHERE is_continuation = true;

-- 缓存响应查询优化
CREATE INDEX IF NOT EXISTS idx_request_logs_cached_response 
ON request_logs(cached_response_id) 
WHERE cached_response_id IS NOT NULL;

-- 超时分析优化
CREATE INDEX IF NOT EXISTS idx_request_logs_timeout_analysis 
ON request_logs(effective_timeout_seconds, latency_ms) 
WHERE effective_timeout_seconds IS NOT NULL;

-- 节点切换分析优化
CREATE INDEX IF NOT EXISTS idx_request_logs_node_switch 
ON request_logs(node_switch_count, created_at DESC) 
WHERE node_switch_count > 0;

-- ============================================================================
-- 4. 创建分析视图
-- ============================================================================

-- 超时效果分析视图
CREATE OR REPLACE VIEW v_timeout_effectiveness AS
SELECT 
    DATE_TRUNC('hour', created_at) as time_bucket,
    timeout_mode,
    COUNT(*) as total_requests,
    COUNT(*) FILTER (WHERE success = true) as success_count,
    COUNT(*) FILTER (WHERE success = false AND err_code LIKE '%timeout%') as timeout_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = false AND err_code LIKE '%timeout%') / COUNT(*), 2) as timeout_rate,
    ROUND(AVG(effective_timeout_seconds), 2) as avg_effective_timeout,
    ROUND(AVG(latency_ms)/1000.0, 2) as avg_latency_seconds
FROM request_logs
WHERE created_at > NOW() - INTERVAL '24 hours'
  AND effective_timeout_seconds IS NOT NULL
GROUP BY time_bucket, timeout_mode
ORDER BY time_bucket DESC;

COMMENT ON VIEW v_timeout_effectiveness IS '超时策略效果分析视图';

-- 继续/重试效果分析视图
CREATE OR REPLACE VIEW v_continuation_effectiveness AS
SELECT 
    DATE_TRUNC('hour', created_at) as time_bucket,
    COUNT(*) FILTER (WHERE is_continuation = true) as continuation_requests,
    COUNT(*) FILTER (WHERE cached_response_id IS NOT NULL) as cache_hits,
    COUNT(*) FILTER (WHERE is_continuation = true AND cached_response_id IS NULL) as cache_misses,
    ROUND(100.0 * COUNT(*) FILTER (WHERE cached_response_id IS NOT NULL) / 
        NULLIF(COUNT(*) FILTER (WHERE is_continuation = true), 0), 2) as cache_hit_rate,
    SUM(COALESCE(context_size_tokens, 0)) FILTER (WHERE cached_response_id IS NOT NULL) as tokens_saved
FROM request_logs
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY time_bucket
HAVING COUNT(*) FILTER (WHERE is_continuation = true) > 0
ORDER BY time_bucket DESC;

COMMENT ON VIEW v_continuation_effectiveness IS '继续/重试缓存效果分析视图';

-- 节点切换分析视图
CREATE OR REPLACE VIEW v_node_switch_analysis AS
SELECT 
    DATE_TRUNC('hour', created_at) as time_bucket,
    node_switch_count as switches,
    COUNT(*) as request_count,
    COUNT(*) FILTER (WHERE success = true) as success_count,
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / COUNT(*), 2) as success_rate,
    ROUND(AVG(latency_ms)/1000.0, 2) as avg_latency_seconds
FROM request_logs
WHERE created_at > NOW() - INTERVAL '24 hours'
  AND node_switch_count >= 0
GROUP BY time_bucket, node_switch_count
ORDER BY time_bucket DESC, node_switch_count;

COMMENT ON VIEW v_node_switch_analysis IS '节点切换分析视图';

-- ============================================================================
-- 5. 创建查询辅助函数
-- ============================================================================

-- 获取会话最后一次成功请求
CREATE OR REPLACE FUNCTION get_last_successful_request(p_session_id VARCHAR, p_minutes INT DEFAULT 60)
RETURNS TABLE (
    request_id BIGINT,
    created_at TIMESTAMPTZ,
    latency_ms INT,
    response_text TEXT
) AS $$
BEGIN
    RETURN QUERY
    SELECT 
        id as request_id,
        r.created_at,
        r.latency_ms,
        r.response_text
    FROM request_logs r
    WHERE r.session_id = p_session_id
      AND r.success = true
      AND r.created_at > NOW() - (p_minutes || ' minutes')::INTERVAL
    ORDER BY r.created_at DESC
    LIMIT 1;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION get_last_successful_request(VARCHAR, INT) IS '获取会话最后一次成功请求（用于继续/重试检测）';

-- ============================================================================
-- 6. 数据迁移：为现有数据填充默认值
-- ============================================================================

-- 为现有记录设置默认值（批量更新，避免长事务）
DO $$
DECLARE
    batch_size INT := 10000;
    updated_count INT := 0;
    total_updated INT := 0;
BEGIN
    LOOP
        -- 批量更新
        WITH to_update AS (
            SELECT id
            FROM request_logs
            WHERE is_continuation IS NULL
            LIMIT batch_size
        )
        UPDATE request_logs r
        SET 
            is_continuation = false,
            node_switch_count = 0,
            keepalive_sent_count = 0
        FROM to_update
        WHERE r.id = to_update.id;
        
        GET DIAGNOSTICS updated_count = ROW_COUNT;
        total_updated := total_updated + updated_count;
        
        -- 如果更新行数为0，说明没有更多记录需要更新
        EXIT WHEN updated_count = 0;
        
        -- 每批次后提交并休眠，避免锁表
        PERFORM pg_sleep(0.1);
    END LOOP;
    
    RAISE NOTICE 'Migrated % existing records', total_updated;
END $$;

-- ============================================================================
-- 7. 验证
-- ============================================================================

DO $$
DECLARE
    column_count INT;
BEGIN
    -- 验证新字段是否存在
    SELECT COUNT(*) INTO column_count
    FROM information_schema.columns
    WHERE table_name = 'request_logs'
      AND column_name IN (
        'effective_timeout_seconds',
        'context_size_tokens',
        'is_continuation',
        'cached_response_id',
        'node_switch_count',
        'keepalive_sent_count'
      );
    
    IF column_count < 6 THEN
        RAISE EXCEPTION 'Not all columns were added to request_logs';
    END IF;
    
    RAISE NOTICE 'Migration 002_extend_request_logs completed successfully';
    RAISE NOTICE 'Added % new columns to request_logs', column_count;
END $$;

-- 显示表结构信息
SELECT 
    column_name,
    data_type,
    is_nullable,
    column_default
FROM information_schema.columns
WHERE table_name = 'request_logs'
  AND column_name IN (
    'effective_timeout_seconds',
    'context_size_tokens',
    'timeout_mode',
    'is_continuation',
    'continuation_keywords',
    'cached_response_id',
    'node_switch_count',
    'keepalive_sent_count'
  )
ORDER BY ordinal_position;
