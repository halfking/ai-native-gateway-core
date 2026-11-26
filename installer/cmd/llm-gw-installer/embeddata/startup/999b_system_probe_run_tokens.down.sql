-- Migration: 999b_system_probe_run_tokens (down)
-- Purpose: 回滚 999b —— 删掉 system_probe_runs.total_tokens 列
--
-- ★ 只删本迁移自己加的列；表的其余列与数据不动。
-- ★ 回滚后写侧 INSERT 会重新变成 42703（bg/systemmonitor/audit.go 仍写这一列）
--   ⇒ 回滚必须与代码回滚同批，否则探针审计行会静默丢失。
-- ★ 锁守卫：down 件同样含 ALTER TABLE ⇒ 同样声明 lock_timeout（同 up 件）。

\set ON_ERROR_STOP on

SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

ALTER TABLE public.system_probe_runs DROP COLUMN IF EXISTS total_tokens;

DELETE FROM public.schema_migrations WHERE version = '999b';

COMMIT;
