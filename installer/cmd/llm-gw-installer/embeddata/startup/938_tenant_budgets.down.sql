-- Down for 938: drop tenant_budgets.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.tenant_budgets;
DELETE FROM public.schema_migrations WHERE version = '938';
COMMIT;
