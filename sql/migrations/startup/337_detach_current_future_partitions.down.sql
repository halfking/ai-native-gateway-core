-- Migration 337 rollback: 重新 ATTACH 当月及未来月度分区
--
-- 警告：
--   回滚后，*_default 表将无法接收时间戳落在月度分区范围内的数据。
--   这会导致 INSERT INTO request_logs_default ... 报 partition constraint 错误。
--   仅在测试或需要恢复原有 ATTACHED 状态时执行。

BEGIN;

-- 1. request_logs: 重新 ATTACH 2026-07 至 2026-12
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs_2026_07') THEN
        ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_07 
        FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED request_logs_2026_07';
    END IF;

    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs_2026_08') THEN
        ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_08 
        FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED request_logs_2026_08';
    END IF;

    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs_2026_09') THEN
        ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_09 
        FOR VALUES FROM ('2026-09-01 00:00:00+00') TO ('2026-10-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED request_logs_2026_09';
    END IF;

    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs_2026_10') THEN
        ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_10 
        FOR VALUES FROM ('2026-10-01 00:00:00+00') TO ('2026-11-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED request_logs_2026_10';
    END IF;

    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs_2026_11') THEN
        ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_11 
        FOR VALUES FROM ('2026-11-01 00:00:00+00') TO ('2026-12-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED request_logs_2026_11';
    END IF;

    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs_2026_12') THEN
        ALTER TABLE request_logs ATTACH PARTITION request_logs_2026_12 
        FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED request_logs_2026_12';
    END IF;
END
$$;

-- 2. request_logs_bodies
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs_bodies_2026_07') THEN
        ALTER TABLE request_logs_bodies ATTACH PARTITION request_logs_bodies_2026_07 
        FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED request_logs_bodies_2026_07';
    END IF;

    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs_bodies_2026_08') THEN
        ALTER TABLE request_logs_bodies ATTACH PARTITION request_logs_bodies_2026_08 
        FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED request_logs_bodies_2026_08';
    END IF;
END
$$;

-- 3. request_wal
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal_2026_07') THEN
        ALTER TABLE request_wal ATTACH PARTITION request_wal_2026_07 
        FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED request_wal_2026_07';
    END IF;

    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal_2026_08') THEN
        ALTER TABLE request_wal ATTACH PARTITION request_wal_2026_08 
        FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED request_wal_2026_08';
    END IF;
END
$$;

-- 4. usage_ledger
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'usage_ledger_2026_07') THEN
        ALTER TABLE usage_ledger ATTACH PARTITION usage_ledger_2026_07 
        FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED usage_ledger_2026_07';
    END IF;

    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'usage_ledger_2026_08') THEN
        ALTER TABLE usage_ledger ATTACH PARTITION usage_ledger_2026_08 
        FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED usage_ledger_2026_08';
    END IF;
END
$$;

-- 5. routing_decision_log
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'routing_decision_log_2026_07') THEN
        ALTER TABLE routing_decision_log ATTACH PARTITION routing_decision_log_2026_07 
        FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED routing_decision_log_2026_07';
    END IF;

    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'routing_decision_log_2026_08') THEN
        ALTER TABLE routing_decision_log ATTACH PARTITION routing_decision_log_2026_08 
        FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED routing_decision_log_2026_08';
    END IF;
END
$$;

-- 6. credential_model_index
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'credential_model_index_2026_07') THEN
        ALTER TABLE credential_model_index ATTACH PARTITION credential_model_index_2026_07 
        FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED credential_model_index_2026_07';
    END IF;

    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'credential_model_index_2026_08') THEN
        ALTER TABLE credential_model_index ATTACH PARTITION credential_model_index_2026_08 
        FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');
        RAISE NOTICE 'RE-ATTACHED credential_model_index_2026_08';
    END IF;
END
$$;

COMMIT;
