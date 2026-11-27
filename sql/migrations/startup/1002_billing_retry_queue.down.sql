-- ============================================================
-- 迁移 1002 回滚: 删除计费重试队列
-- ============================================================


-- 锁守卫（installer 门 startup_ddl_lock_guard_gate_test.go：版本 > 870 且含强锁 DDL
--   ⇒ 必须自带 lock_timeout）：本件含 ALTER TABLE ＋ CREATE INDEX ⇒ 声明
--   5s lock_timeout / 10min statement_timeout（同 999a/999d 口径）。
SET lock_timeout = '5s';
SET statement_timeout = '10min';

DROP VIEW IF EXISTS v_billing_retry_stats;

DROP TABLE IF EXISTS billing_retry_queue;

-- ★ 台账自登记的回滚半边（与 up 件的 INSERT 对称；633.down 同款）。
DELETE FROM public.schema_migrations WHERE version = '1002';
