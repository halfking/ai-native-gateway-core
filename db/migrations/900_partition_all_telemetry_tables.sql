-- ============================================================================
-- Telemetry 表分区设计方案 - 完整实施脚本
-- ============================================================================
-- 目标：实现主表（heap）+ 归档表（columnar）的架构
-- 原则：当月数据 heap，历史数据 columnar，自动归档
-- ============================================================================

-- ============================================================================
-- 1. usage_ledger 分区表设计
-- ============================================================================

-- 当前状态：单表 heap，7 行数据，1 个月
-- 目标：按月分区，支持 UPDATE，可归档为 columnar

BEGIN;

-- 1.1 创建分区父表
CREATE TABLE usage_ledger_partitioned (
    request_id text NOT NULL,
    ts timestamptz NOT NULL,
    tenant_id text NOT NULL,
    application_id int,
    api_key_id int,
    end_user_id text,
    credential_id int,
    provider_id int,
    canonical_id int,
    raw_model_name text,
    prompt_tokens int,
    completion_tokens int,
    cache_read_tokens int,
    cache_write_tokens int,
    total_tokens int,
    cost_usd numeric(12,6),
    latency_ms int,
    success boolean,
    error_kind text,
    -- 唯一约束包含分区键
    UNIQUE (request_id, ts)
) PARTITION BY RANGE (ts);

-- 1.2 创建索引（在父表上，自动继承到所有分区）
CREATE INDEX idx_usage_ledger_part_request_id ON usage_ledger_partitioned (request_id);
CREATE INDEX idx_usage_ledger_part_ts ON usage_ledger_partitioned (ts);
CREATE INDEX idx_usage_ledger_part_tenant ON usage_ledger_partitioned (tenant_id, ts);

-- 1.3 创建当月分区（heap）
CREATE TABLE usage_ledger_2026_06
PARTITION OF usage_ledger_partitioned
FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00')
USING heap;

-- 1.4 创建下月分区（heap）
CREATE TABLE usage_ledger_2026_07
PARTITION OF usage_ledger_partitioned
FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00')
USING heap;

-- 1.5 迁移现有数据
INSERT INTO usage_ledger_partitioned 
SELECT * FROM usage_ledger
ON CONFLICT (request_id, ts) DO NOTHING;

-- 1.6 验证数据
SELECT 
    'usage_ledger' as old_table, COUNT(*) as count 
FROM usage_ledger
UNION ALL
SELECT 
    'usage_ledger_partitioned' as new_table, COUNT(*) as count 
FROM usage_ledger_partitioned;

-- 1.7 切换表名
ALTER TABLE usage_ledger RENAME TO usage_ledger_old;
ALTER TABLE usage_ledger_partitioned RENAME TO usage_ledger;

-- 1.8 清理（确认无误后执行）
-- DROP TABLE usage_ledger_old;

COMMIT;

COMMENT ON TABLE usage_ledger IS '请求使用量账本 - 按月分区，当月 heap，历史 columnar';

-- ============================================================================
-- 2. credit_ledger 分区表设计
-- ============================================================================

-- 当前状态：单表，0 行数据
-- 特点：没有 ts 字段，使用 created_at
-- 目标：按月分区（基于 created_at）

BEGIN;

-- 2.1 创建分区父表
CREATE TABLE credit_ledger_partitioned (
    id bigserial,
    tenant_id varchar NOT NULL,
    entry_type varchar NOT NULL,
    amount bigint NOT NULL,
    balance_after bigint NOT NULL,
    ref_type varchar,
    ref_id varchar,
    note text,
    created_at timestamptz NOT NULL DEFAULT now(),
    pool varchar,
    -- 唯一约束包含分区键
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- 2.2 创建索引
CREATE INDEX idx_credit_ledger_part_tenant ON credit_ledger_partitioned (tenant_id, created_at);
CREATE INDEX idx_credit_ledger_part_ref ON credit_ledger_partitioned (ref_type, ref_id);
CREATE INDEX idx_credit_ledger_part_created ON credit_ledger_partitioned (created_at);

-- 2.3 创建当月分区（heap）
CREATE TABLE credit_ledger_2026_06
PARTITION OF credit_ledger_partitioned
FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00')
USING heap;

-- 2.4 创建下月分区（heap）
CREATE TABLE credit_ledger_2026_07
PARTITION OF credit_ledger_partitioned
FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00')
USING heap;

-- 2.5 迁移现有数据
INSERT INTO credit_ledger_partitioned 
SELECT * FROM credit_ledger
ON CONFLICT (id, created_at) DO NOTHING;

-- 2.6 切换表名
ALTER TABLE credit_ledger RENAME TO credit_ledger_old;
ALTER TABLE credit_ledger_partitioned RENAME TO credit_ledger;

-- 2.7 更新序列（保持 ID 连续性）
SELECT setval('credit_ledger_partitioned_id_seq', 
              COALESCE((SELECT MAX(id) FROM credit_ledger), 1));

COMMIT;

COMMENT ON TABLE credit_ledger IS '积分账本 - 按月分区（基于 created_at），当月 heap，历史 columnar';

-- ============================================================================
-- 3. tool_usage_stats 分区表设计
-- ============================================================================

-- 当前状态：单表，0 行数据
-- 特点：使用 created_at，有 usage_date（按天统计）
-- 目标：按月分区（基于 created_at）

BEGIN;

-- 3.1 创建分区父表
CREATE TABLE tool_usage_stats_partitioned (
    id bigserial,
    tool_id varchar NOT NULL,
    tenant_id varchar NOT NULL,
    usage_date date NOT NULL,
    call_count bigint DEFAULT 0,
    success_count bigint DEFAULT 0,
    error_count bigint DEFAULT 0,
    avg_latency_ms int,
    last_called_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz,
    -- 唯一约束：同一天同一工具的统计唯一
    UNIQUE (tool_id, tenant_id, usage_date, created_at),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- 3.2 创建索引
CREATE INDEX idx_tool_stats_part_tool ON tool_usage_stats_partitioned (tool_id, usage_date);
CREATE INDEX idx_tool_stats_part_tenant ON tool_usage_stats_partitioned (tenant_id, usage_date);
CREATE INDEX idx_tool_stats_part_date ON tool_usage_stats_partitioned (usage_date);
CREATE INDEX idx_tool_stats_part_created ON tool_usage_stats_partitioned (created_at);

-- 3.3 创建当月分区（heap）
CREATE TABLE tool_usage_stats_2026_06
PARTITION OF tool_usage_stats_partitioned
FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00')
USING heap;

-- 3.4 创建下月分区（heap）
CREATE TABLE tool_usage_stats_2026_07
PARTITION OF tool_usage_stats_partitioned
FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00')
USING heap;

-- 3.5 迁移现有数据
INSERT INTO tool_usage_stats_partitioned 
SELECT * FROM tool_usage_stats
ON CONFLICT (tool_id, tenant_id, usage_date, created_at) DO NOTHING;

-- 3.6 切换表名
ALTER TABLE tool_usage_stats RENAME TO tool_usage_stats_old;
ALTER TABLE tool_usage_stats_partitioned RENAME TO tool_usage_stats;

-- 3.7 更新序列
SELECT setval('tool_usage_stats_partitioned_id_seq', 
              COALESCE((SELECT MAX(id) FROM tool_usage_stats), 1));

COMMIT;

COMMENT ON TABLE tool_usage_stats IS '工具使用统计 - 按月分区（基于 created_at），当月 heap，历史 columnar';

-- ============================================================================
-- 4. 验证所有分区表
-- ============================================================================

-- 4.1 检查分区表结构
SELECT 
    parent.relname as table_name,
    child.relname as partition_name,
    pg_get_expr(child.relpartbound, child.oid) as partition_range,
    am.amname as storage_type,
    pg_size_pretty(pg_relation_size(child.oid)) as size
FROM pg_inherits
JOIN pg_class parent ON pg_inherits.inhparent = parent.oid
JOIN pg_class child ON pg_inherits.inhrelid = child.oid
LEFT JOIN pg_am am ON child.relam = am.oid
WHERE parent.relname IN ('request_logs', 'usage_ledger', 'credit_ledger', 'tool_usage_stats')
ORDER BY parent.relname, child.relname;

-- 4.2 检查数据分布
SELECT 
    'request_logs' as table_name,
    COUNT(*) as total_rows,
    MIN(ts) as earliest,
    MAX(ts) as latest
FROM request_logs
UNION ALL
SELECT 
    'usage_ledger',
    COUNT(*),
    MIN(ts),
    MAX(ts)
FROM usage_ledger
UNION ALL
SELECT 
    'credit_ledger',
    COUNT(*),
    MIN(created_at),
    MAX(created_at)
FROM credit_ledger
UNION ALL
SELECT 
    'tool_usage_stats',
    COUNT(*),
    MIN(created_at),
    MAX(created_at)
FROM tool_usage_stats;

-- 4.3 检查存储类型
SELECT 
    c.relname as table_or_partition,
    c.relkind,
    am.amname as storage_type,
    pg_size_pretty(pg_relation_size(c.oid)) as size
FROM pg_class c
LEFT JOIN pg_am am ON c.relam = am.oid
WHERE c.relname LIKE '%_ledger%' 
   OR c.relname LIKE '%_stats%'
   OR c.relname LIKE '%request_logs%'
ORDER BY c.relname;

-- ============================================================================
-- 5. 创建月度分区的辅助函数
-- ============================================================================

CREATE OR REPLACE FUNCTION create_next_month_partitions()
RETURNS void AS $$
DECLARE
    next_month_start date;
    next_month_end date;
    month_suffix text;
BEGIN
    -- 计算下个月的开始和结束
    next_month_start := date_trunc('month', now() + interval '1 month');
    next_month_end := date_trunc('month', now() + interval '2 months');
    month_suffix := to_char(next_month_start, 'YYYY_MM');
    
    -- request_logs 分区已存在，跳过
    
    -- usage_ledger
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS usage_ledger_%s
        PARTITION OF usage_ledger
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    
    -- credit_ledger
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS credit_ledger_%s
        PARTITION OF credit_ledger
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    
    -- tool_usage_stats
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS tool_usage_stats_%s
        PARTITION OF tool_usage_stats
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    
    RAISE NOTICE 'Created partitions for %', month_suffix;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION create_next_month_partitions() IS '自动创建下个月的分区（heap 存储）';

-- 测试函数
SELECT create_next_month_partitions();

-- ============================================================================
-- 6. 创建归档函数（将上月分区转换为 columnar）
-- ============================================================================

CREATE OR REPLACE FUNCTION archive_last_month_partitions()
RETURNS void AS $$
DECLARE
    last_month_suffix text;
    partition_name text;
    temp_name text;
    row_count bigint;
BEGIN
    last_month_suffix := to_char(date_trunc('month', now() - interval '1 month'), 'YYYY_MM');
    
    RAISE NOTICE 'Archiving partitions for %', last_month_suffix;
    
    -- 归档 usage_ledger
    partition_name := 'usage_ledger_' || last_month_suffix;
    temp_name := partition_name || '_archive';
    
    IF EXISTS (SELECT 1 FROM pg_tables WHERE tablename = partition_name) THEN
        -- 获取行数
        EXECUTE format('SELECT COUNT(*) FROM %I', partition_name) INTO row_count;
        RAISE NOTICE 'Archiving % (%s rows)', partition_name, row_count;
        
        -- 创建 columnar 副本
        EXECUTE format('
            CREATE TABLE %I 
            USING columnar 
            AS SELECT * FROM %I',
            temp_name, partition_name);
        
        -- 删除原 heap 分区
        EXECUTE format('DROP TABLE %I', partition_name);
        
        -- 重命名
        EXECUTE format('ALTER TABLE %I RENAME TO %I', temp_name, partition_name);
        
        RAISE NOTICE 'Archived % to columnar', partition_name;
    END IF;
    
    -- 归档 credit_ledger
    partition_name := 'credit_ledger_' || last_month_suffix;
    temp_name := partition_name || '_archive';
    
    IF EXISTS (SELECT 1 FROM pg_tables WHERE tablename = partition_name) THEN
        EXECUTE format('SELECT COUNT(*) FROM %I', partition_name) INTO row_count;
        RAISE NOTICE 'Archiving % (%s rows)', partition_name, row_count;
        
        EXECUTE format('
            CREATE TABLE %I 
            USING columnar 
            AS SELECT * FROM %I',
            temp_name, partition_name);
        
        EXECUTE format('DROP TABLE %I', partition_name);
        EXECUTE format('ALTER TABLE %I RENAME TO %I', temp_name, partition_name);
        
        RAISE NOTICE 'Archived % to columnar', partition_name;
    END IF;
    
    -- 归档 tool_usage_stats
    partition_name := 'tool_usage_stats_' || last_month_suffix;
    temp_name := partition_name || '_archive';
    
    IF EXISTS (SELECT 1 FROM pg_tables WHERE tablename = partition_name) THEN
        EXECUTE format('SELECT COUNT(*) FROM %I', partition_name) INTO row_count;
        RAISE NOTICE 'Archiving % (%s rows)', partition_name, row_count;
        
        EXECUTE format('
            CREATE TABLE %I 
            USING columnar 
            AS SELECT * FROM %I',
            temp_name, partition_name);
        
        EXECUTE format('DROP TABLE %I', partition_name);
        EXECUTE format('ALTER TABLE %I RENAME TO %I', temp_name, partition_name);
        
        RAISE NOTICE 'Archived % to columnar', partition_name;
    END IF;
    
    -- 归档 request_logs
    partition_name := 'request_logs_' || last_month_suffix;
    temp_name := partition_name || '_archive';
    
    IF EXISTS (SELECT 1 FROM pg_tables WHERE tablename = partition_name) THEN
        EXECUTE format('SELECT COUNT(*) FROM %I', partition_name) INTO row_count;
        RAISE NOTICE 'Archiving % (%s rows)', partition_name, row_count;
        
        EXECUTE format('
            CREATE TABLE %I 
            USING columnar 
            AS SELECT * FROM %I',
            temp_name, partition_name);
        
        EXECUTE format('DROP TABLE %I', partition_name);
        EXECUTE format('ALTER TABLE %I RENAME TO %I', temp_name, partition_name);
        
        RAISE NOTICE 'Archived % to columnar', partition_name;
    END IF;
    
    RAISE NOTICE 'Archive complete for %', last_month_suffix;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION archive_last_month_partitions() IS '将上月的 heap 分区转换为 columnar 存储（每月1日执行）';

-- ============================================================================
-- 7. 使用说明
-- ============================================================================

/*
每月自动任务：

1. 每月 1 日凌晨 1:00 - 创建下个月的分区
   SELECT create_next_month_partitions();

2. 每月 1 日凌晨 2:00 - 归档上月的分区
   SELECT archive_last_month_partitions();

Cron 任务示例：
0 1 1 * * psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "SELECT create_next_month_partitions();"
0 2 1 * * psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "SELECT archive_last_month_partitions();"

查询使用：
- 应用代码无需修改，PostgreSQL 自动路由到正确的分区
- WHERE ts >= '2026-06-01' 会自动只扫描相关分区
- UPDATE/DELETE 在 heap 分区中正常工作
- columnar 分区只读，不支持 UPDATE/DELETE

存储优化：
- heap: 当月实时数据，支持 UPDATE
- columnar: 历史归档，5-10x 压缩，只读
- 预计节省 80% 存储空间
*/

-- ============================================================================
-- 结束
-- ============================================================================
