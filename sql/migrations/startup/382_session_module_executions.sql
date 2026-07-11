-- 382_session_module_executions.sql
-- 会话模块执行记录系统（hot 表 + 分区表）
-- 目的：避免重复执行相同的模块检测/分析任务，提升性能，降低 LLM/DB 压力
--
-- 设计原则：
--   1. Hot 表：保留最近 7 天执行记录，支持高频查询
--   2. 分区表：按月归档历史记录，支持长期审计和数据分析
--   3. Check-Execute-Record 模式：执行前检查，已有结果直接复用
--   4. TTL 机制：结果可设置有效期，避免永久缓存导致的问题
--   5. 版本控制：module_version 字段支持模块升级时自动失效旧结果

BEGIN;

-- ============================================================
-- 1. Hot 表：session_module_executions_hot
-- ============================================================
CREATE TABLE IF NOT EXISTS session_module_executions_hot (
    -- 主键
    execution_id BIGSERIAL PRIMARY KEY,
    
    -- 会话维度
    gw_session_id VARCHAR(128) NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    
    -- 模块标识
    module_name VARCHAR(100) NOT NULL,
    module_version VARCHAR(20),
    
    -- 执行上下文
    request_id VARCHAR(128),
    batch_key VARCHAR(255) DEFAULT '',
    
    -- 执行状态
    status VARCHAR(20) NOT NULL DEFAULT 'running',
    -- pending: 待执行
    -- running: 执行中
    -- completed: 执行成功
    -- failed: 执行失败
    -- skipped: 已跳过（有有效缓存）
    
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    duration_ms INT,
    
    -- 执行结果
    result_summary JSONB,
    result_detail JSONB,
    error_message TEXT,
    
    -- 缓存控制
    cache_key VARCHAR(255) NOT NULL,
    ttl_seconds INT NOT NULL DEFAULT 3600,
    expires_at TIMESTAMPTZ NOT NULL,
    
    -- 审计字段
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 唯一约束：同一会话+模块+批次+开始时间只允许一条
    CONSTRAINT uk_sme_hot_session_module_batch 
        UNIQUE (gw_session_id, module_name, batch_key, started_at)
);

-- 核心查询索引：查找有效缓存
CREATE INDEX IF NOT EXISTS idx_sme_hot_lookup 
    ON session_module_executions_hot(gw_session_id, module_name, cache_key, status, expires_at)
    WHERE status = 'completed';

-- 租户查询索引
CREATE INDEX IF NOT EXISTS idx_sme_hot_tenant_time 
    ON session_module_executions_hot(tenant_id, created_at DESC);

-- 模块统计索引
CREATE INDEX IF NOT EXISTS idx_sme_hot_module_stats 
    ON session_module_executions_hot(module_name, status, completed_at DESC);

-- 清理索引：定期删除过期记录
CREATE INDEX IF NOT EXISTS idx_sme_hot_cleanup 
    ON session_module_executions_hot(created_at);

-- 状态查询索引：查找运行中或失败的执行
CREATE INDEX IF NOT EXISTS idx_sme_hot_status 
    ON session_module_executions_hot(status, started_at)
    WHERE status IN ('running', 'failed');


-- ============================================================
-- 2. 分区表：session_module_executions（按月归档）
-- ============================================================
CREATE TABLE IF NOT EXISTS session_module_executions (
    execution_id BIGINT NOT NULL,
    gw_session_id VARCHAR(128) NOT NULL,
    tenant_id VARCHAR(255) NOT NULL,
    module_name VARCHAR(100) NOT NULL,
    module_version VARCHAR(20),
    request_id VARCHAR(128),
    batch_key VARCHAR(255) DEFAULT '',
    status VARCHAR(20) NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    duration_ms INT,
    result_summary JSONB,
    result_detail JSONB,
    error_message TEXT,
    cache_key VARCHAR(255) NOT NULL,
    ttl_seconds INT NOT NULL DEFAULT 3600,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    
    PRIMARY KEY (execution_id, created_at)
) PARTITION BY RANGE (created_at);

-- 创建当前月份分区
DO $$
DECLARE
    current_month_start DATE;
    current_month_end DATE;
    partition_name TEXT;
    next_month_start DATE;
    next_month_end DATE;
    next_partition_name TEXT;
BEGIN
    -- 当前月
    current_month_start := DATE_TRUNC('month', NOW());
    current_month_end := current_month_start + INTERVAL '1 month';
    partition_name := 'session_module_executions_' || TO_CHAR(current_month_start, 'YYYY_MM');
    
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS %I PARTITION OF session_module_executions
        FOR VALUES FROM (%L) TO (%L)
    ', partition_name, current_month_start, current_month_end);
    
    EXECUTE format('
        CREATE INDEX IF NOT EXISTS idx_%s_session 
        ON %I(gw_session_id, module_name)
    ', partition_name, partition_name);
    
    EXECUTE format('
        CREATE INDEX IF NOT EXISTS idx_%s_tenant 
        ON %I(tenant_id, created_at DESC)
    ', partition_name, partition_name);
    
    -- 下个月（提前创建）
    next_month_start := current_month_start + INTERVAL '1 month';
    next_month_end := next_month_start + INTERVAL '1 month';
    next_partition_name := 'session_module_executions_' || TO_CHAR(next_month_start, 'YYYY_MM');
    
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS %I PARTITION OF session_module_executions
        FOR VALUES FROM (%L) TO (%L)
    ', next_partition_name, next_month_start, next_month_end);
    
    EXECUTE format('
        CREATE INDEX IF NOT EXISTS idx_%s_session 
        ON %I(gw_session_id, module_name)
    ', next_partition_name, next_partition_name);
    
    EXECUTE format('
        CREATE INDEX IF NOT EXISTS idx_%s_tenant 
        ON %I(tenant_id, created_at DESC)
    ', next_partition_name, next_partition_name);
END $$;


-- ============================================================
-- 3. 归档函数：从 hot 表迁移到分区表
-- ============================================================
CREATE OR REPLACE FUNCTION archive_session_module_executions(retention_days INT DEFAULT 7)
RETURNS TABLE(archived_count BIGINT) AS $$
DECLARE
    v_count BIGINT;
BEGIN
    -- 将过期数据插入分区表
    WITH archived AS (
        DELETE FROM session_module_executions_hot
        WHERE created_at < NOW() - (retention_days || ' days')::INTERVAL
        RETURNING *
    )
    INSERT INTO session_module_executions
    SELECT * FROM archived;
    
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN QUERY SELECT v_count;
END;
$$ LANGUAGE plpgsql;


-- ============================================================
-- 4. 自动分区创建函数
-- ============================================================
CREATE OR REPLACE FUNCTION ensure_session_module_executions_partition(
    target_date DATE DEFAULT NULL
)
RETURNS TEXT AS $$
DECLARE
    v_date DATE;
    v_month_start DATE;
    v_month_end DATE;
    v_partition_name TEXT;
    v_next_partition_name TEXT;
    v_next_month_start DATE;
    v_next_month_end DATE;
BEGIN
    v_date := COALESCE(target_date, NOW());
    v_month_start := DATE_TRUNC('month', v_date);
    v_month_end := v_month_start + INTERVAL '1 month';
    v_partition_name := 'session_module_executions_' || TO_CHAR(v_month_start, 'YYYY_MM');
    
    -- 创建目标月分区
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = v_partition_name) THEN
        EXECUTE format('
            CREATE TABLE %I PARTITION OF session_module_executions
            FOR VALUES FROM (%L) TO (%L)
        ', v_partition_name, v_month_start, v_month_end);
        
        EXECUTE format('
            CREATE INDEX idx_%s_session ON %I(gw_session_id, module_name)
        ', v_partition_name, v_partition_name);
        
        EXECUTE format('
            CREATE INDEX idx_%s_tenant ON %I(tenant_id, created_at DESC)
        ', v_partition_name, v_partition_name);
    END IF;
    
    -- 同时确保下个月分区也存在
    v_next_month_start := v_month_start + INTERVAL '1 month';
    v_next_month_end := v_next_month_start + INTERVAL '1 month';
    v_next_partition_name := 'session_module_executions_' || TO_CHAR(v_next_month_start, 'YYYY_MM');
    
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = v_next_partition_name) THEN
        EXECUTE format('
            CREATE TABLE %I PARTITION OF session_module_executions
            FOR VALUES FROM (%L) TO (%L)
        ', v_next_partition_name, v_next_month_start, v_next_month_end);
        
        EXECUTE format('
            CREATE INDEX idx_%s_session ON %I(gw_session_id, module_name)
        ', v_next_partition_name, v_next_partition_name);
        
        EXECUTE format('
            CREATE INDEX idx_%s_tenant ON %I(tenant_id, created_at DESC)
        ', v_next_partition_name, v_next_partition_name);
    END IF;
    
    RETURN v_partition_name;
END;
$$ LANGUAGE plpgsql;


-- ============================================================
-- 5. 监控视图
-- ============================================================

-- 模块执行统计（最近 24 小时）
CREATE OR REPLACE VIEW v_sme_module_stats AS
SELECT 
    module_name,
    status,
    COUNT(*) as execution_count,
    AVG(duration_ms)::INT as avg_duration_ms,
    PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY duration_ms)::INT as p50_duration_ms,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms)::INT as p95_duration_ms,
    PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY duration_ms)::INT as p99_duration_ms,
    COUNT(DISTINCT gw_session_id) as unique_sessions,
    COUNT(*) FILTER (WHERE created_at > NOW() - INTERVAL '1 hour') as executions_last_hour
FROM session_module_executions_hot
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY module_name, status;

-- 缓存命中率统计
CREATE OR REPLACE VIEW v_sme_cache_hit_rate AS
SELECT 
    module_name,
    COUNT(*) FILTER (WHERE status = 'completed') as total_executions,
    COUNT(*) FILTER (WHERE status = 'skipped') as cache_skips,
    ROUND(
        COUNT(*) FILTER (WHERE status = 'skipped') * 100.0 / 
        NULLIF(COUNT(*), 0), 2
    ) as skip_rate_pct
FROM session_module_executions_hot
WHERE created_at > NOW() - INTERVAL '24 hours'
GROUP BY module_name;

-- 失败执行监控
CREATE OR REPLACE VIEW v_sme_failures AS
SELECT 
    module_name,
    COUNT(*) as failure_count,
    MAX(created_at) as last_failure_at,
    array_agg(DISTINCT error_message) FILTER (WHERE error_message IS NOT NULL) as error_messages
FROM session_module_executions_hot
WHERE status = 'failed'
  AND created_at > NOW() - INTERVAL '24 hours'
GROUP BY module_name
HAVING COUNT(*) > 0;


-- ============================================================
-- 6. 注释
-- ============================================================
COMMENT ON TABLE session_module_executions_hot IS 
    '会话模块执行记录热表 - 记录每个会话对每个模块的执行情况，避免重复执行（保留 7 天）';

COMMENT ON COLUMN session_module_executions_hot.module_name IS 
    '模块标识，参考 domains/moduleregistry/constants.go';

COMMENT ON COLUMN session_module_executions_hot.cache_key IS 
    '输入参数哈希，用于判断是否可复用之前的执行结果';

COMMENT ON COLUMN session_module_executions_hot.expires_at IS 
    '结果过期时间，超过此时间视为无效';

COMMENT ON COLUMN session_module_executions_hot.result_summary IS 
    '结果摘要（轻量 JSONB），用于快速判断和展示';

COMMENT ON COLUMN session_module_executions_hot.result_detail IS 
    '结果详情（完整 JSONB），供后续模块使用';

COMMENT ON TABLE session_module_executions IS 
    '会话模块执行记录归档表 - 按月分区，保留历史数据供审计和长期分析';


-- ============================================================
-- 7. pg_cron 定时任务（如果扩展可用）
-- ============================================================

-- 每日凌晨 2 点归档 7 天前的数据
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_cron') THEN
        PERFORM cron.schedule(
            'archive-sme-daily',
            '0 2 * * *',
            'SELECT archive_session_module_executions(7)'
        );
        
        -- 每月 1 号确保下个月分区存在
        PERFORM cron.schedule(
            'ensure-sme-partition-monthly',
            '0 0 1 * *',
            'SELECT ensure_session_module_executions_partition()'
        );
    END IF;
END $$;

-- The module executor writes asynchronously through pooled connections and
-- cannot rely on a transaction-local app.current_tenant setting.
DROP POLICY IF EXISTS tenant_isolation_session_module_executions_hot
    ON public.session_module_executions_hot;
DROP POLICY IF EXISTS tenant_isolation_session_module_executions
    ON public.session_module_executions;
ALTER TABLE public.session_module_executions_hot DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.session_module_executions DISABLE ROW LEVEL SECURITY;

ALTER TABLE public.session_module_executions_hot
    DROP CONSTRAINT IF EXISTS chk_sme_hot_status;
ALTER TABLE public.session_module_executions_hot
    ADD CONSTRAINT chk_sme_hot_status
    CHECK (status IN ('pending', 'running', 'completed', 'failed', 'skipped')) NOT VALID;
ALTER TABLE public.session_module_executions
    DROP CONSTRAINT IF EXISTS chk_sme_status;
ALTER TABLE public.session_module_executions
    ADD CONSTRAINT chk_sme_status
    CHECK (status IN ('pending', 'running', 'completed', 'failed', 'skipped')) NOT VALID;
CREATE INDEX IF NOT EXISTS idx_sme_hot_cleanup
    ON public.session_module_executions_hot (created_at);

COMMIT;
