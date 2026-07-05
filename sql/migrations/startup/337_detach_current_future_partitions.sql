-- Migration 337: DETACH 当月及未来月度分区以启用 DEFAULT 分区写入
--
-- 问题根因：
--   PostgreSQL DEFAULT 分区的约束是动态的：
--     - 当月度分区（如 request_logs_2026_07）ATTACHED 时，DEFAULT 分区
--       会自动排除该分区覆盖的时间范围 [2026-07-01, 2026-08-01)。
--     - 这导致所有落在当月范围内的 INSERT INTO request_logs_default
--       失败，报错：new row violates partition constraint (SQLSTATE 23514)。
--
-- 解决方案：
--   DETACH 当月及未来的月度分区（2026-07 至 2026-12），使 DEFAULT 分区
--   可以接收所有新数据。历史月度分区（如 2026-06 及更早）保持 ATTACHED
--   以便 SELECT 父表时自动聚合历史数据。
--
-- 架构依据：
--   参照 docs/partition/ 的架构方案：
--     1. 所有新数据写入 *_default（heap 表，支持 UPDATE/DELETE）
--     2. 月度分区 DETACHED（不参与 PG 自动路由）
--     3. 后台迁移器定期将 *_default 中 > 7 天的数据搬运到对应月度分区
--     4. 月度分区可使用 columnar 压缩（历史数据只读）
--
-- 适用表：
--   - request_logs
--   - request_logs_bodies
--   - request_wal
--   - usage_ledger
--   - routing_decision_log
--   - credential_model_index
--
-- 未来新增的分区表（credit_ledger、tool_usage_stats）在 migration 334/335
-- 中已预设为 DETACHED 创建，无需在本 migration 中处理。

BEGIN;

-- 1. request_logs: DETACH 当月及未来分区（2026-07 至 2026-12）
DO $$
BEGIN
    -- 2026-07
    IF EXISTS (
        SELECT 1 FROM pg_inherits i
        JOIN pg_class c ON i.inhrelid = c.oid
        JOIN pg_class p ON i.inhparent = p.oid
        WHERE p.relname = 'request_logs' AND c.relname = 'request_logs_2026_07'
    ) THEN
        ALTER TABLE request_logs DETACH PARTITION request_logs_2026_07;
        RAISE NOTICE 'DETACHED request_logs_2026_07';
    END IF;

    -- 2026-08
    IF EXISTS (
        SELECT 1 FROM pg_inherits i
        JOIN pg_class c ON i.inhrelid = c.oid
        JOIN pg_class p ON i.inhparent = p.oid
        WHERE p.relname = 'request_logs' AND c.relname = 'request_logs_2026_08'
    ) THEN
        ALTER TABLE request_logs DETACH PARTITION request_logs_2026_08;
        RAISE NOTICE 'DETACHED request_logs_2026_08';
    END IF;

    -- 2026-09
    IF EXISTS (
        SELECT 1 FROM pg_inherits i
        JOIN pg_class c ON i.inhrelid = c.oid
        JOIN pg_class p ON i.inhparent = p.oid
        WHERE p.relname = 'request_logs' AND c.relname = 'request_logs_2026_09'
    ) THEN
        ALTER TABLE request_logs DETACH PARTITION request_logs_2026_09;
        RAISE NOTICE 'DETACHED request_logs_2026_09';
    END IF;

    -- 2026-10
    IF EXISTS (
        SELECT 1 FROM pg_inherits i
        JOIN pg_class c ON i.inhrelid = c.oid
        JOIN pg_class p ON i.inhparent = p.oid
        WHERE p.relname = 'request_logs' AND c.relname = 'request_logs_2026_10'
    ) THEN
        ALTER TABLE request_logs DETACH PARTITION request_logs_2026_10;
        RAISE NOTICE 'DETACHED request_logs_2026_10';
    END IF;

    -- 2026-11
    IF EXISTS (
        SELECT 1 FROM pg_inherits i
        JOIN pg_class c ON i.inhrelid = c.oid
        JOIN pg_class p ON i.inhparent = p.oid
        WHERE p.relname = 'request_logs' AND c.relname = 'request_logs_2026_11'
    ) THEN
        ALTER TABLE request_logs DETACH PARTITION request_logs_2026_11;
        RAISE NOTICE 'DETACHED request_logs_2026_11';
    END IF;

    -- 2026-12
    IF EXISTS (
        SELECT 1 FROM pg_inherits i
        JOIN pg_class c ON i.inhrelid = c.oid
        JOIN pg_class p ON i.inhparent = p.oid
        WHERE p.relname = 'request_logs' AND c.relname = 'request_logs_2026_12'
    ) THEN
        ALTER TABLE request_logs DETACH PARTITION request_logs_2026_12;
        RAISE NOTICE 'DETACHED request_logs_2026_12';
    END IF;
END
$$;

-- 2. request_logs_bodies: DETACH 当月及未来分区（如果表存在）
DO $$
BEGIN
    -- 检查父表是否存在
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_logs_bodies' AND relkind = 'p') THEN
        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'request_logs_bodies' AND c.relname = 'request_logs_bodies_2026_07'
        ) THEN
            ALTER TABLE request_logs_bodies DETACH PARTITION request_logs_bodies_2026_07;
            RAISE NOTICE 'DETACHED request_logs_bodies_2026_07';
        END IF;

        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'request_logs_bodies' AND c.relname = 'request_logs_bodies_2026_08'
        ) THEN
            ALTER TABLE request_logs_bodies DETACH PARTITION request_logs_bodies_2026_08;
            RAISE NOTICE 'DETACHED request_logs_bodies_2026_08';
        END IF;
    END IF;
END
$$;

-- 3. request_wal: DETACH 当月及未来分区（如果存在）
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'request_wal' AND relkind = 'p') THEN
        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'request_wal' AND c.relname = 'request_wal_2026_07'
        ) THEN
            ALTER TABLE request_wal DETACH PARTITION request_wal_2026_07;
            RAISE NOTICE 'DETACHED request_wal_2026_07';
        END IF;

        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'request_wal' AND c.relname = 'request_wal_2026_08'
        ) THEN
            ALTER TABLE request_wal DETACH PARTITION request_wal_2026_08;
            RAISE NOTICE 'DETACHED request_wal_2026_08';
        END IF;
    END IF;
END
$$;

-- 4. usage_ledger: DETACH 当月及未来分区（如果存在）
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'usage_ledger' AND relkind = 'p') THEN
        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'usage_ledger' AND c.relname = 'usage_ledger_2026_07'
        ) THEN
            ALTER TABLE usage_ledger DETACH PARTITION usage_ledger_2026_07;
            RAISE NOTICE 'DETACHED usage_ledger_2026_07';
        END IF;

        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'usage_ledger' AND c.relname = 'usage_ledger_2026_08'
        ) THEN
            ALTER TABLE usage_ledger DETACH PARTITION usage_ledger_2026_08;
            RAISE NOTICE 'DETACHED usage_ledger_2026_08';
        END IF;
    END IF;
END
$$;

-- 5. routing_decision_log: DETACH 当月及未来分区（如果存在）
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'routing_decision_log' AND relkind = 'p') THEN
        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'routing_decision_log' AND c.relname = 'routing_decision_log_2026_07'
        ) THEN
            ALTER TABLE routing_decision_log DETACH PARTITION routing_decision_log_2026_07;
            RAISE NOTICE 'DETACHED routing_decision_log_2026_07';
        END IF;

        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'routing_decision_log' AND c.relname = 'routing_decision_log_2026_08'
        ) THEN
            ALTER TABLE routing_decision_log DETACH PARTITION routing_decision_log_2026_08;
            RAISE NOTICE 'DETACHED routing_decision_log_2026_08';
        END IF;
    END IF;
END
$$;

-- 6. credential_model_index: DETACH 当月及未来分区（如果存在）
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'credential_model_index' AND relkind = 'p') THEN
        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'credential_model_index' AND c.relname = 'credential_model_index_2026_07'
        ) THEN
            ALTER TABLE credential_model_index DETACH PARTITION credential_model_index_2026_07;
            RAISE NOTICE 'DETACHED credential_model_index_2026_07';
        END IF;

        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'credential_model_index' AND c.relname = 'credential_model_index_2026_08'
        ) THEN
            ALTER TABLE credential_model_index DETACH PARTITION credential_model_index_2026_08;
            RAISE NOTICE 'DETACHED credential_model_index_2026_08';
        END IF;
    END IF;
END
$$;

-- 7. credit_ledger: DETACH 当月及未来分区（如果存在）
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'credit_ledger' AND relkind = 'p') THEN
        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'credit_ledger' AND c.relname = 'credit_ledger_2026_07'
        ) THEN
            ALTER TABLE credit_ledger DETACH PARTITION credit_ledger_2026_07;
            RAISE NOTICE 'DETACHED credit_ledger_2026_07';
        END IF;

        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'credit_ledger' AND c.relname = 'credit_ledger_2026_08'
        ) THEN
            ALTER TABLE credit_ledger DETACH PARTITION credit_ledger_2026_08;
            RAISE NOTICE 'DETACHED credit_ledger_2026_08';
        END IF;
    END IF;
END
$$;

-- 8. tool_usage_stats: DETACH 当月及未来分区（如果存在）
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'tool_usage_stats' AND relkind = 'p') THEN
        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'tool_usage_stats' AND c.relname = 'tool_usage_stats_2026_07'
        ) THEN
            ALTER TABLE tool_usage_stats DETACH PARTITION tool_usage_stats_2026_07;
            RAISE NOTICE 'DETACHED tool_usage_stats_2026_07';
        END IF;

        IF EXISTS (
            SELECT 1 FROM pg_inherits i
            JOIN pg_class c ON i.inhrelid = c.oid
            JOIN pg_class p ON i.inhparent = p.oid
            WHERE p.relname = 'tool_usage_stats' AND c.relname = 'tool_usage_stats_2026_08'
        ) THEN
            ALTER TABLE tool_usage_stats DETACH PARTITION tool_usage_stats_2026_08;
            RAISE NOTICE 'DETACHED tool_usage_stats_2026_08';
        END IF;
    END IF;
END
$$;

COMMIT;

-- 验证：
--   SELECT c.relname, pg_get_expr(c.relpartbound, c.oid) AS partition_bound
--   FROM pg_class c
--   JOIN pg_inherits i ON c.oid = i.inhrelid
--   JOIN pg_class p ON i.inhparent = p.oid
--   WHERE p.relname = 'request_logs'
--   ORDER BY c.relname;
--
-- 预期结果：
--   只有 request_logs_default（以及可能的历史分区如 2026_06）仍 ATTACHED。
--   2026-07 至 2026-12 已 DETACHED，不在查询结果中。
--
-- 写入验证：
--   INSERT INTO request_logs_default (request_id, ts, tenant_id, success)
--   VALUES ('test-req-003', NOW(), 'test-tenant-03', true);
--
--   应成功插入，不再报 partition constraint 错误。
