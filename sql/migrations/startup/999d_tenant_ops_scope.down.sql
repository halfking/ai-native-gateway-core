-- Migration: 999d_tenant_ops_scope (down)
-- Purpose: 回滚 999d —— 删掉 licenses / releases 的 tenant_id 列与两个索引
--
-- ★ 只删本迁移自己加的列与索引；两表的其余列与数据不动。
-- ★ 回滚后 tenantops 的两个接口会重新变成 42703 ⇒ 回滚必须与代码回滚同批。
-- ★ 锁守卫：down 件同样含 ALTER TABLE ＋ DROP INDEX ⇒ 同样声明 lock_timeout（同 up 件）。

\set ON_ERROR_STOP on

SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

DROP INDEX IF EXISTS public.idx_licenses_tenant_created;
DROP INDEX IF EXISTS public.idx_releases_tenant_published;

ALTER TABLE public.licenses DROP COLUMN IF EXISTS tenant_id;
ALTER TABLE public.releases DROP COLUMN IF EXISTS tenant_id;

DELETE FROM public.schema_migrations WHERE version = '999d';

COMMIT;
