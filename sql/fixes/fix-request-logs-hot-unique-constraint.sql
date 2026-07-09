-- 修复 request_logs_hot 表的唯一约束
-- 问题：INSERT ON CONFLICT (request_id, ts) 需要唯一约束，但表中没有

BEGIN;

-- 添加唯一约束（如果不存在）
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint 
        WHERE conrelid = 'request_logs_hot'::regclass 
        AND contype = 'u'
    ) THEN
        ALTER TABLE request_logs_hot
            ADD CONSTRAINT uq_request_logs_hot_request_id_ts 
            UNIQUE (request_id, ts);
        RAISE NOTICE 'Added unique constraint on (request_id, ts)';
    ELSE
        RAISE NOTICE 'Unique constraint already exists';
    END IF;
END $$;

-- 验证
DO $$
DECLARE
    constraint_count INT;
BEGIN
    SELECT count(*) INTO constraint_count
    FROM pg_constraint 
    WHERE conrelid = 'request_logs_hot'::regclass 
    AND contype = 'u';
    
    RAISE NOTICE 'Unique constraints on request_logs_hot: %', constraint_count;
END $$;

COMMIT;

\echo ''
\echo 'Unique constraint fix complete'
\echo ''
