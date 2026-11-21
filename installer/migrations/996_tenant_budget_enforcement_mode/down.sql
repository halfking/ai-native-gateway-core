-- Down for 1000: drop enforcement_mode（只退本迁移引入的列与约束；
-- 表本体与 938 的其余列保留 —— 迁移链每一步只回退自己引入的那部分）。
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
ALTER TABLE public.tenant_budgets DROP COLUMN IF EXISTS enforcement_mode;
DELETE FROM public.schema_migrations WHERE version = '996';
COMMIT;
