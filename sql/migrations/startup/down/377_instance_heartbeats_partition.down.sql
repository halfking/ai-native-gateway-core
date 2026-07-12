-- 377_instance_heartbeats_partition.down.sql
-- 撤销 instance_heartbeats 分区表

BEGIN;

-- 删除分区表及其分区
DROP TABLE IF EXISTS instance_heartbeats CASCADE;

-- 如果有备份表，恢复回去
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_tables 
        WHERE schemaname = 'public' 
        AND tablename = 'instance_heartbeats_old_backup'
    ) THEN
        ALTER TABLE instance_heartbeats_old_backup RENAME TO instance_heartbeats;
        RAISE NOTICE 'Restored instance_heartbeats from backup';
    ELSE
        -- 没有备份，重新创建原始表结构（兼容 377_center_ops.sql）
        CREATE TABLE IF NOT EXISTS instance_heartbeats (
            instance_id     TEXT NOT NULL,
            timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
            uptime_secs     BIGINT NOT NULL,
            num_goroutine   INT NOT NULL,
            alloc_mb        DOUBLE PRECISION NOT NULL,
            status          TEXT NOT NULL,
            PRIMARY KEY (instance_id, timestamp)
        );
        
        CREATE INDEX IF NOT EXISTS idx_ih_instance ON instance_heartbeats (instance_id, timestamp DESC);
        CREATE INDEX IF NOT EXISTS idx_ih_timestamp ON instance_heartbeats (timestamp DESC);
        
        RAISE NOTICE 'Recreated original instance_heartbeats table';
    END IF;
END $$;

COMMIT;
