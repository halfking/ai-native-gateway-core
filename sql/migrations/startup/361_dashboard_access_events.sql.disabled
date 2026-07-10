-- 361_dashboard_access_events.sql
-- Dashboard API 访问事件埋点表（hot + 分区归档）
--
-- 目的：
--   1. 记录 Dashboard API 的所有访问（PV/UV/响应时间）
--   2. 追踪慢查询、错误率
--   3. 分析用户行为，优化 API 设计
--   4. 支持业务监控和告警
--
-- 数据生命周期：
--   - Hot 表：保留 30 天
--   - 归档表：按月分区，长期保留（用于审计和长期分析）

BEGIN;

-- ============================================================
-- 1. Hot 表：dashboard_access_events_hot
-- ============================================================
CREATE TABLE IF NOT EXISTS dashboard_access_events_hot (
    -- 主键
    event_id VARCHAR(64) PRIMARY KEY,
    
    -- 事件类型
    event_type VARCHAR(20) NOT NULL,  -- api_access / query / export / error
    
    -- 时间戳
    timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 用户信息
    tenant_id VARCHAR(255) NOT NULL,
    user_id VARCHAR(255),
    user_role VARCHAR(50),  -- super_admin / tenant_admin / user
    session_id VARCHAR(128),  -- 用户会话 ID
    
    -- API 信息
    api_path VARCHAR(255) NOT NULL,
    api_method VARCHAR(10) NOT NULL,
    api_version VARCHAR(20),
    
    -- 请求参数（脱敏后）
    query_params JSONB,
    
    -- 响应信息
    status_code INT NOT NULL,
    response_time_ms INT NOT NULL,
    cache_hit BOOLEAN DEFAULT FALSE,
    data_size INT,  -- 返回数据大小（字节）
    
    -- 错误信息
    error_code VARCHAR(50),
    error_message TEXT,
    
    -- 客户端信息
    client_ip INET,
    user_agent TEXT,
    referer TEXT,
    
    -- 性能分解
    db_query_time_ms INT,
    cache_query_time_ms INT,
    
    -- 审计字段
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 索引：按时间范围查询
CREATE INDEX IF NOT EXISTS idx_dae_hot_timestamp 
    ON dashboard_access_events_hot(timestamp DESC);

-- 索引：按租户和时间查询
CREATE INDEX IF NOT EXISTS idx_dae_hot_tenant_time 
    ON dashboard_access_events_hot(tenant_id, timestamp DESC);

-- 索引：按 API 路径查询
CREATE INDEX IF NOT EXISTS idx_dae_hot_api_time 
    ON dashboard_access_events_hot(api_path, timestamp DESC);

-- 索引：按用户查询
CREATE INDEX IF NOT EXISTS idx_dae_hot_user_time 
    ON dashboard_access_events_hot(user_id, timestamp DESC) 
    WHERE user_id IS NOT NULL;

-- 索引：按事件类型查询
CREATE INDEX IF NOT EXISTS idx_dae_hot_event_type 
    ON dashboard_access_events_hot(event_type, timestamp DESC);

-- 索引：错误查询
CREATE INDEX IF NOT EXISTS idx_dae_hot_errors 
    ON dashboard_access_events_hot(timestamp DESC) 
    WHERE status_code >= 400;

-- 索引：慢查询监控
CREATE INDEX IF NOT EXISTS idx_dae_hot_slow 
    ON dashboard_access_events_hot(response_time_ms DESC, timestamp DESC) 
    WHERE response_time_ms > 1000;

-- 索引：清理（30天前的数据）
CREATE INDEX IF NOT EXISTS idx_dae_hot_cleanup 
    ON dashboard_access_events_hot(created_at);


-- ============================================================
-- 2. 归档表：dashboard_access_events（按月分区）
-- ============================================================
CREATE TABLE IF NOT EXISTS dashboard_access_events (
    event_id VARCHAR(64) NOT NULL,
    event_type VARCHAR(20) NOT NULL,
    timestamp TIMESTAMPTZ NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    user_id VARCHAR(255),
    user_role VARCHAR(50),
    session_id VARCHAR(128),
    api_path VARCHAR(255) NOT NULL,
    api_method VARCHAR(10) NOT NULL,
    api_version VARCHAR(20),
    query_params JSONB,
    status_code INT NOT NULL,
    response_time_ms INT NOT NULL,
    cache_hit BOOLEAN DEFAULT FALSE,
    data_size INT,
    error_code VARCHAR(50),
    error_message TEXT,
    client_ip INET,
    user_agent TEXT,
    referer TEXT,
    db_query_time_ms INT,
    cache_query_time_ms INT,
    created_at TIMESTAMPTZ NOT NULL,
    
    PRIMARY KEY (event_id, created_at)
) PARTITION BY RANGE (created_at);

-- 创建当前月和下个月分区
DO $$
DECLARE
    current_month_start DATE;
    current_month_end DATE;
    partition_name TEXT;
    next_month_start DATE;
    next_month_end DATE;
    next_partition_name TEXT;
BEGIN
    current_month_start := DATE_TRUNC('month', NOW());
    current_month_end := current_month_start + INTERVAL '1 month';
    partition_name := 'dashboard_access_events_' || TO_CHAR(current_month_start, 'YYYY_MM');
    
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS %I PARTITION OF dashboard_access_events
        FOR VALUES FROM (%L) TO (%L)
    ', partition_name, current_month_start, current_month_end);
    
    EXECUTE format('
        CREATE INDEX IF NOT EXISTS idx_%s_tenant ON %I(tenant_id, timestamp DESC)
    ', partition_name, partition_name);
    
    next_month_start := current_month_start + INTERVAL '1 month';
    next_month_end := next_month_start + INTERVAL '1 month';
    next_partition_name := 'dashboard_access_events_' || TO_CHAR(next_month_start, 'YYYY_MM');
    
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS %I PARTITION OF dashboard_access_events
        FOR VALUES FROM (%L) TO (%L)
    ', next_partition_name, next_month_start, next_month_end);
    
    EXECUTE format('
        CREATE INDEX IF NOT EXISTS idx_%s_tenant ON %I(tenant_id, timestamp DESC)
    ', next_partition_name, next_partition_name);
END $$;


-- ============================================================
-- 3. 归档函数（hot → 分区表）
-- ============================================================
CREATE OR REPLACE FUNCTION archive_dashboard_events(retention_days INT DEFAULT 30)
RETURNS BIGINT AS $$
DECLARE
    v_count BIGINT;
BEGIN
    WITH archived AS (
        DELETE FROM dashboard_access_events_hot
        WHERE created_at < NOW() - (retention_days || ' days')::INTERVAL
        RETURNING *
    )
    INSERT INTO dashboard_access_events
    SELECT * FROM archived;
    
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN v_count;
END;
$$ LANGUAGE plpgsql;


-- ============================================================
-- 4. 自动分区创建函数
-- ============================================================
CREATE OR REPLACE FUNCTION ensure_dashboard_events_partition(target_date DATE DEFAULT NULL)
RETURNS TEXT AS $$
DECLARE
    v_date DATE;
    v_month_start DATE;
    v_month_end DATE;
    v_partition_name TEXT;
BEGIN
    v_date := COALESCE(target_date, NOW());
    v_month_start := DATE_TRUNC('month', v_date);
    v_month_end := v_month_start + INTERVAL '1 month';
    v_partition_name := 'dashboard_access_events_' || TO_CHAR(v_month_start, 'YYYY_MM');
    
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = v_partition_name) THEN
        EXECUTE format('
            CREATE TABLE %I PARTITION OF dashboard_access_events
            FOR VALUES FROM (%L) TO (%L)
        ', v_partition_name, v_month_start, v_month_end);
        
        EXECUTE format('
            CREATE INDEX idx_%s_tenant ON %I(tenant_id, timestamp DESC)
        ', v_partition_name, v_partition_name);
    END IF;
    
    RETURN v_partition_name;
END;
$$ LANGUAGE plpgsql;


-- ============================================================
-- 5. 监控视图
-- ============================================================

-- API 访问统计
CREATE OR REPLACE VIEW v_dashboard_access_stats AS
SELECT 
    api_path,
    event_type,
    COUNT(*) as request_count,
    COUNT(DISTINCT user_id) as unique_users,
    COUNT(DISTINCT tenant_id) as unique_tenants,
    AVG(response_time_ms)::FLOAT as avg_response_ms,
    PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY response_time_ms)::FLOAT as p50_ms,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY response_time_ms)::FLOAT as p95_ms,
    PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY response_time_ms)::FLOAT as p99_ms,
    COUNT(*) FILTER (WHERE cache_hit = true) * 100.0 / NULLIF(COUNT(*), 0) as cache_hit_rate,
    COUNT(*) FILTER (WHERE status_code >= 400) * 100.0 / NULLIF(COUNT(*), 0) as error_rate,
    COUNT(*) FILTER (WHERE response_time_ms > 1000) as slow_query_count,
    MAX(timestamp) as last_access_at
FROM dashboard_access_events_hot
WHERE timestamp > NOW() - INTERVAL '24 hours'
GROUP BY api_path, event_type
ORDER BY request_count DESC;

-- 慢查询监控
CREATE OR REPLACE VIEW v_dashboard_slow_queries AS
SELECT 
    api_path,
    api_method,
    tenant_id,
    user_id,
    response_time_ms,
    timestamp,
    error_code,
    error_message
FROM dashboard_access_events_hot
WHERE response_time_ms > 1000
  AND timestamp > NOW() - INTERVAL '24 hours'
ORDER BY response_time_ms DESC
LIMIT 100;

-- 错误监控
CREATE OR REPLACE VIEW v_dashboard_errors AS
SELECT 
    api_path,
    error_code,
    COUNT(*) as error_count,
    MAX(timestamp) as last_error_at,
    array_agg(DISTINCT error_message) FILTER (WHERE error_message IS NOT NULL) as error_messages
FROM dashboard_access_events_hot
WHERE status_code >= 400
  AND timestamp > NOW() - INTERVAL '24 hours'
GROUP BY api_path, error_code
HAVING COUNT(*) > 0
ORDER BY error_count DESC;

-- 用户活跃度
CREATE OR REPLACE VIEW v_dashboard_user_activity AS
SELECT 
    user_id,
    tenant_id,
    user_role,
    COUNT(*) as request_count,
    COUNT(DISTINCT api_path) as unique_apis,
    MAX(timestamp) as last_activity_at,
    NOW() - MAX(timestamp) as idle_duration
FROM dashboard_access_events_hot
WHERE timestamp > NOW() - INTERVAL '7 days'
  AND user_id IS NOT NULL
GROUP BY user_id, tenant_id, user_role
ORDER BY last_activity_at DESC;


-- ============================================================
-- 6. pg_cron 定时任务
-- ============================================================
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_cron') THEN
        -- 每日凌晨 3 点归档 30 天前的数据
        PERFORM cron.schedule(
            'archive-dashboard-events-daily',
            '0 3 * * *',
            $$SELECT archive_dashboard_events(30)$$
        );
        
        -- 每月 1 号确保下个月分区存在
        PERFORM cron.schedule(
            'ensure-dashboard-partition-monthly',
            '0 0 1 * *',
            $$SELECT ensure_dashboard_events_partition()$$
        );
    END IF;
END $$;


-- ============================================================
-- 7. 注释
-- ============================================================
COMMENT ON TABLE dashboard_access_events_hot IS 
    'Dashboard API 访问事件热表 - 记录所有 Dashboard 相关 API 的访问情况（保留 30 天）';

COMMENT ON COLUMN dashboard_access_events_hot.event_type IS 
    '事件类型：api_access（API 访问）/ query（数据查询）/ export（数据导出）/ error（错误）';

COMMENT ON COLUMN dashboard_access_events_hot.query_params IS 
    '请求参数（JSONB，需要脱敏处理后再存储）';

COMMENT ON COLUMN dashboard_access_events_hot.response_time_ms IS 
    '响应时间（毫秒），用于慢查询监控';

COMMENT ON TABLE dashboard_access_events IS 
    'Dashboard API 访问事件归档表 - 按月分区，长期保留用于审计和分析';

COMMIT;