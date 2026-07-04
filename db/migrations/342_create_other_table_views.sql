-- Migration 342: 为其他表创建 with_current_month 查询视图（修复版）
--
-- 背景：
--   migration 337 DETACH 了当月及未来月度分区，使 *_default 可以接收写入。
--   但这导致 SELECT * FROM <parent> 不再包含 DETACHED 月份的数据。
--   migration 341 为 request_logs 创建了 2 路 UNION 视图（hot + parent），
--   其他表仍需类似视图以支持跨月聚合查询。
--
-- 视图规范：
--   *_with_current_month = UNION ALL [<parent>, <current_month_detached>]
--   注：一些表的当月分区可能不存在，需用 DO 块动态构造
--
-- 维护：
--   每月 1 号需要更新视图加入新的当前月份（手动或自动 cron）

BEGIN;

-- ============================================================
-- 1. usage_ledger_with_current_month
-- ============================================================
DROP VIEW IF EXISTS usage_ledger_with_current_month;
CREATE OR REPLACE VIEW usage_ledger_with_current_month AS
SELECT * FROM usage_ledger UNION ALL SELECT * FROM usage_ledger_2026_07;

COMMENT ON VIEW usage_ledger_with_current_month IS
'Cross-month query VIEW for usage_ledger.';

-- ============================================================
-- 2. routing_decision_log_with_current_month
-- ============================================================
DROP VIEW IF EXISTS routing_decision_log_with_current_month;
CREATE OR REPLACE VIEW routing_decision_log_with_current_month AS
SELECT * FROM routing_decision_log UNION ALL SELECT * FROM routing_decision_log_2026_07;

COMMENT ON VIEW routing_decision_log_with_current_month IS
'Cross-month query VIEW for routing_decision_log.';

-- ============================================================
-- 3. request_wal_with_current_month
-- ============================================================
DROP VIEW IF EXISTS request_wal_with_current_month;
CREATE OR REPLACE VIEW request_wal_with_current_month AS
SELECT * FROM request_wal UNION ALL SELECT * FROM request_wal_2026_07;

COMMENT ON VIEW request_wal_with_current_month IS
'Cross-month query VIEW for request_wal.';

-- ============================================================
-- 4. request_logs_bodies_with_current_month
-- ============================================================
DROP VIEW IF EXISTS request_logs_bodies_with_current_month;
CREATE OR REPLACE VIEW request_logs_bodies_with_current_month AS
SELECT * FROM request_logs_bodies UNION ALL SELECT * FROM request_logs_bodies_2026_07;

COMMENT ON VIEW request_logs_bodies_with_current_month IS
'Cross-month query VIEW for request_logs_bodies.';

-- ============================================================
-- 5. credential_model_index_with_current_month (动态)
-- ============================================================
-- credential_model_index_2026_07 可能不存在（取决于 ensure_partition 是否已运行）
DROP VIEW IF EXISTS credential_model_index_with_current_month;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'credential_model_index_2026_07') THEN
        EXECUTE 'CREATE VIEW credential_model_index_with_current_month AS SELECT * FROM credential_model_index UNION ALL SELECT * FROM credential_model_index_2026_07';
    ELSE
        EXECUTE 'CREATE VIEW credential_model_index_with_current_month AS SELECT * FROM credential_model_index';
    END IF;
END $$;

COMMENT ON VIEW credential_model_index_with_current_month IS
'Cross-month query VIEW for credential_model_index (dynamically includes 2026_07 if it exists).';

-- ============================================================
-- 6. credit_ledger_with_current_month
-- ============================================================
DROP VIEW IF EXISTS credit_ledger_with_current_month;
CREATE OR REPLACE VIEW credit_ledger_with_current_month AS
SELECT * FROM credit_ledger UNION ALL SELECT * FROM credit_ledger_2026_07;

COMMENT ON VIEW credit_ledger_with_current_month IS
'Cross-month query VIEW for credit_ledger.';

-- ============================================================
-- 7. tool_usage_stats_with_current_month
-- ============================================================
DROP VIEW IF EXISTS tool_usage_stats_with_current_month;
CREATE OR REPLACE VIEW tool_usage_stats_with_current_month AS
SELECT * FROM tool_usage_stats UNION ALL SELECT * FROM tool_usage_stats_2026_07;

COMMENT ON VIEW tool_usage_stats_with_current_month IS
'Cross-month query VIEW for tool_usage_stats.';

COMMIT;

-- 验证
DO $$
DECLARE
    view_count int;
BEGIN
    SELECT COUNT(*) INTO view_count
    FROM pg_views
    WHERE viewname LIKE '%_with_current_month';
    
    RAISE NOTICE 'Migration 342: % *_with_current_month views created', view_count;
END
$$;