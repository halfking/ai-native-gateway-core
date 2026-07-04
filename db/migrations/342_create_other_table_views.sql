-- Migration 342: 为其他表创建 with_current_month 查询视图
--
-- 背景：
--   migration 337 DETACH 了当月及未来月度分区，使 *_default 可以接收写入。
--   但这导致 SELECT * FROM <parent> 不再包含 DETACHED 月份的数据。
--   migration 341 为 request_logs 创建了 2 路 UNION 视图（hot + parent），
--   其他表仍需类似视图以支持跨月聚合查询。
--
-- 视图规范：
--   *_with_current_month = UNION ALL [<parent>, <current_month_detached>]
--   其中 _default 在父表或当前月份分区中已包含
--
-- 维护：
--   每月 1 号需要更新视图加入新的当前月份（手动或自动 cron）

BEGIN;

-- ============================================================
-- usage_ledger_with_current_month
-- ============================================================
DROP VIEW IF EXISTS usage_ledger_with_current_month;
CREATE OR REPLACE VIEW usage_ledger_with_current_month AS
SELECT * FROM usage_ledger UNION ALL SELECT * FROM usage_ledger_2026_07;

COMMENT ON VIEW usage_ledger_with_current_month IS
'Cross-month query VIEW for usage_ledger. Aggregates parent table (ATTACHED
historical partitions) and current month DETACHED partition (2026_07).
Created by migration 342 (2026-07-05).';

-- ============================================================
-- routing_decision_log_with_current_month
-- ============================================================
DROP VIEW IF EXISTS routing_decision_log_with_current_month;
CREATE OR REPLACE VIEW routing_decision_log_with_current_month AS
SELECT * FROM routing_decision_log UNION ALL SELECT * FROM routing_decision_log_2026_07;

COMMENT ON VIEW routing_decision_log_with_current_month IS
'Cross-month query VIEW for routing_decision_log.';

-- ============================================================
-- request_wal_with_current_month
-- ============================================================
DROP VIEW IF EXISTS request_wal_with_current_month;
CREATE OR REPLACE VIEW request_wal_with_current_month AS
SELECT * FROM request_wal UNION ALL SELECT * FROM request_wal_2026_07;

COMMENT ON VIEW request_wal_with_current_month IS
'Cross-month query VIEW for request_wal.';

-- ============================================================
-- request_logs_bodies_with_current_month
-- ============================================================
DROP VIEW IF EXISTS request_logs_bodies_with_current_month;
CREATE OR REPLACE VIEW request_logs_bodies_with_current_month AS
SELECT * FROM request_logs_bodies UNION ALL SELECT * FROM request_logs_bodies_2026_07;

COMMENT ON VIEW request_logs_bodies_with_current_month IS
'Cross-month query VIEW for request_logs_bodies.';

-- ============================================================
-- credential_model_index_with_current_month
-- ============================================================
DROP VIEW IF EXISTS credential_model_index_with_current_month;
CREATE OR REPLACE VIEW credential_model_index_with_current_month AS
SELECT * FROM credential_model_index UNION ALL SELECT * FROM credential_model_index_2026_07;

COMMENT ON VIEW credential_model_index_with_current_month IS
'Cross-month query VIEW for credential_model_index.';

-- ============================================================
-- credit_ledger_with_current_month
-- ============================================================
DROP VIEW IF EXISTS credit_ledger_with_current_month;
CREATE OR REPLACE VIEW credit_ledger_with_current_month AS
SELECT * FROM credit_ledger UNION ALL SELECT * FROM credit_ledger_2026_07;

COMMENT ON VIEW credit_ledger_with_current_month IS
'Cross-month query VIEW for credit_ledger.';

-- ============================================================
-- tool_usage_stats_with_current_month
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
    
    IF view_count < 7 THEN
        RAISE WARNING 'Expected 7 with_current_month views, found %', view_count;
    END IF;
    
    RAISE NOTICE 'Migration 342: % *_with_current_month views created/updated', view_count;
END
$$;