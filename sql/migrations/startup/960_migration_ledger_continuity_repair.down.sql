-- 960_migration_ledger_continuity_repair.down.sql —— 回滚记账修复：
-- 删 960 自身与四个回填行（恢复修复前的断层记账态 = down 的语义）。
-- ⚠ 保留对象（约束/索引/表）不动 —— 它们属于 950/951/957/958 的 DDL，
--   其 down 才负责剥对象；本迁移只管记账行。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

DELETE FROM public.schema_migrations WHERE version = '950';
DELETE FROM public.schema_migrations WHERE version = '951';
DELETE FROM public.schema_migrations WHERE version = '957';
DELETE FROM public.schema_migrations WHERE version = '958';
DELETE FROM public.schema_migrations WHERE version = '960';

COMMIT;
