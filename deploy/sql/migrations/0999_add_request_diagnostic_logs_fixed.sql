-- Migration: 添加请求诊断日志表
-- Author: Platform Team
-- Date: 2026-11-18
-- Purpose: 支持请求级别的详细诊断和故障排查

-- 如果表已存在，先删除（开发环境）
DROP TABLE IF EXISTS request_diagnostic_logs CASCADE;

-- 创建分区主表
CREATE TABLE request_diagnostic_logs (
    id BIGSERIAL,
    request_id VARCHAR(255) NOT NULL,
    session_id VARCHAR(255),
    tenant_id VARCHAR(255),
    client_model VARCHAR(255),
    
    start_time TIMESTAMP NOT NULL,
    end_time TIMESTAMP NOT NULL,
    final_status INT NOT NULL,
    success BOOLEAN NOT NULL,
    
    -- JSON字段存储详细信息
    routing_decision JSONB,
    attempts JSONB NOT NULL,
    final_error JSONB,
    
    -- 统计字段（用于查询和聚合）
    total_attempts INT NOT NULL,
    switches INT NOT NULL DEFAULT 0,
    retries INT NOT NULL DEFAULT 0,
    total_duration_ms BIGINT NOT NULL,
    
    created_at TIMESTAMP DEFAULT NOW(),
    
    PRIMARY KEY (id, start_time),
    UNIQUE (request_id, start_time)
) PARTITION BY RANGE (start_time);

-- 创建索引
CREATE INDEX idx_diagnostic_request_id ON request_diagnostic_logs(request_id);
CREATE INDEX idx_diagnostic_session_id ON request_diagnostic_logs(session_id) WHERE session_id IS NOT NULL;
CREATE INDEX idx_diagnostic_tenant_id ON request_diagnostic_logs(tenant_id) WHERE tenant_id IS NOT NULL;
CREATE INDEX idx_diagnostic_start_time ON request_diagnostic_logs(start_time DESC);
CREATE INDEX idx_diagnostic_success ON request_diagnostic_logs(success);
CREATE INDEX idx_diagnostic_final_status ON request_diagnostic_logs(final_status);
CREATE INDEX idx_diagnostic_tenant_time ON request_diagnostic_logs(tenant_id, start_time DESC);

-- 创建今天的分区
CREATE TABLE request_diagnostic_logs_y2026m11d18
    PARTITION OF request_diagnostic_logs
    FOR VALUES FROM ('2026-11-18 00:00:00') TO ('2026-11-19 00:00:00');

-- 创建明天的分区（提前准备）
CREATE TABLE request_diagnostic_logs_y2026m11d19
    PARTITION OF request_diagnostic_logs
    FOR VALUES FROM ('2026-11-19 00:00:00') TO ('2026-11-20 00:00:00');

-- 创建视图：失败请求统计
CREATE OR REPLACE VIEW v_diagnostic_failure_summary AS
SELECT
    DATE_TRUNC('hour', start_time) AS hour,
    tenant_id,
    client_model,
    final_status,
    COUNT(*) AS failure_count,
    AVG(total_attempts) AS avg_attempts,
    AVG(switches) AS avg_switches,
    AVG(total_duration_ms) AS avg_duration_ms
FROM request_diagnostic_logs
WHERE success = false
GROUP BY DATE_TRUNC('hour', start_time), tenant_id, client_model, final_status;

-- 创建视图：切换统计
CREATE OR REPLACE VIEW v_diagnostic_failover_summary AS
SELECT
    DATE_TRUNC('hour', start_time) AS hour,
    tenant_id,
    client_model,
    COUNT(*) AS total_requests,
    SUM(CASE WHEN switches > 0 THEN 1 ELSE 0 END) AS failover_count,
    ROUND(100.0 * SUM(CASE WHEN switches > 0 THEN 1 ELSE 0 END) / COUNT(*), 2) AS failover_rate
FROM request_diagnostic_logs
GROUP BY DATE_TRUNC('hour', start_time), tenant_id, client_model;

-- 添加注释
COMMENT ON TABLE request_diagnostic_logs IS '请求诊断日志：记录每个请求的完整执行过程，包括路由决策、尝试历史、错误详情';
COMMENT ON COLUMN request_diagnostic_logs.request_id IS '请求唯一标识';
COMMENT ON COLUMN request_diagnostic_logs.session_id IS '会话ID（如果有）';
COMMENT ON COLUMN request_diagnostic_logs.routing_decision IS '路由决策详情（JSONB）';
COMMENT ON COLUMN request_diagnostic_logs.attempts IS '尝试历史数组（JSONB）';
COMMENT ON COLUMN request_diagnostic_logs.final_error IS '最终错误详情（JSONB）';
COMMENT ON COLUMN request_diagnostic_logs.switches IS '供应商切换次数';
COMMENT ON COLUMN request_diagnostic_logs.retries IS '重试次数';

-- 创建自动清理函数
CREATE OR REPLACE FUNCTION cleanup_old_diagnostic_logs()
RETURNS void AS $$
DECLARE
    partition_name TEXT;
    cutoff_date DATE;
BEGIN
    -- 删除30天前的分区
    cutoff_date := CURRENT_DATE - INTERVAL '30 days';
    
    FOR partition_name IN
        SELECT tablename
        FROM pg_tables
        WHERE schemaname = 'public'
        AND tablename LIKE 'request_diagnostic_logs_y%'
        AND tablename < 'request_diagnostic_logs_y' || TO_CHAR(cutoff_date, 'YYYYmmdd')
    LOOP
        EXECUTE 'DROP TABLE IF EXISTS ' || partition_name;
        RAISE NOTICE 'Dropped old partition: %', partition_name;
    END LOOP;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION cleanup_old_diagnostic_logs IS '清理30天前的诊断日志分区';
