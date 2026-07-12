-- 377_instance_heartbeats_partition.sql
-- 创建 instance_heartbeats 按月分区表（90 天 TTL）

-- 如果表已存在，先重命名为备份
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_tables 
        WHERE schemaname = 'public' 
        AND tablename = 'instance_heartbeats'
    ) THEN
        -- 检查是否已经是分区表
        IF NOT EXISTS (
            SELECT 1 FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE n.nspname = 'public' 
            AND c.relname = 'instance_heartbeats'
            AND c.relkind = 'p'
        ) THEN
            -- 非分区表，重命名为备份
            ALTER TABLE instance_heartbeats RENAME TO instance_heartbeats_old_backup;
            RAISE NOTICE 'Old table renamed to instance_heartbeats_old_backup';
        END IF;
    END IF;
END $$;

-- 创建分区表（主键包含分区键 timestamp）
CREATE TABLE IF NOT EXISTS instance_heartbeats (
    id              BIGSERIAL,
    instance_id     TEXT NOT NULL,
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
    uptime_secs     BIGINT,
    num_goroutine   INT,
    alloc_mb        DOUBLE PRECISION,
    status          TEXT,
    metrics         JSONB,
    PRIMARY KEY (instance_id, timestamp)
) PARTITION BY RANGE (timestamp);

-- 创建最近 3 个月分区（2026-07 到 2026-09）
CREATE TABLE IF NOT EXISTS instance_heartbeats_2026_07 
    PARTITION OF instance_heartbeats
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

CREATE TABLE IF NOT EXISTS instance_heartbeats_2026_08 
    PARTITION OF instance_heartbeats
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');

CREATE TABLE IF NOT EXISTS instance_heartbeats_2026_09 
    PARTITION OF instance_heartbeats
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');

-- 为分区表创建索引
CREATE INDEX IF NOT EXISTS idx_ih_instance_ts ON instance_heartbeats (instance_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_ih_timestamp ON instance_heartbeats (timestamp DESC);

COMMENT ON TABLE instance_heartbeats IS 'Partitioned by month. Auto-drop partitions older than 90 days. Use pg_cron or manual DROP for old partitions.';
COMMENT ON COLUMN instance_heartbeats.metrics IS 'JSONB for flexible metrics (CPU, memory, goroutines, etc.)';

-- 可选：如果有旧表数据且在 90 天内，迁移数据（生产环境需谨慎评估）
-- CAUTION: 仅在确认数据安全后取消注释
-- INSERT INTO instance_heartbeats (instance_id, timestamp, uptime_secs, num_goroutine, alloc_mb, status)
-- SELECT instance_id, timestamp, uptime_secs, num_goroutine, alloc_mb, status
-- FROM instance_heartbeats_old_backup
-- WHERE timestamp >= now() - interval '90 days'
-- ON CONFLICT (instance_id, timestamp) DO NOTHING;
