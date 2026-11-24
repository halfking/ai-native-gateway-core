-- Rollback script for 983c_add_occurred_cursor_index.sql
--
-- ★ 与 up 的自登记对偶：只退本迁移引入的东西。
-- ★ 不动 request_metrics 表本身、不动任何行 ⇒ 只是去掉一个索引。

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

-- ★ 在父表上 DROP INDEX ⇒ PostgreSQL 自动连带去掉各分区上的同名分区索引。
DROP INDEX IF EXISTS public.idx_request_metrics_occurred_cursor;

-- ============================================================
-- 台账回退（与 up 的自登记对偶）
-- ============================================================
-- down 只退自己引入的行
DELETE FROM public.schema_migrations WHERE version = '983c';

COMMIT;
