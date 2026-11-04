-- Down for 948: drop sales_quote_shares.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.sales_quote_shares;
DELETE FROM public.schema_migrations WHERE version = '948';
COMMIT;
