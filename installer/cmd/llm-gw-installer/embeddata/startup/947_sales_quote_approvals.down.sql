-- Down for 947: drop sales_quote_approvals.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.sales_quote_approvals;
DELETE FROM public.schema_migrations WHERE version = '947';
COMMIT;
