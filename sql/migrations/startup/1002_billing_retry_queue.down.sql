-- ============================================================
-- 迁移 1002 回滚: 删除计费重试队列
-- ============================================================

DROP VIEW IF EXISTS v_billing_retry_stats;

DROP TABLE IF EXISTS billing_retry_queue;
