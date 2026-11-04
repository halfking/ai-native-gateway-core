-- Down for 944: drop sales_quote_status_log + sales_quotes.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.sales_quote_status_log;
DROP TABLE IF EXISTS public.sales_quotes;
DELETE FROM public.schema_migrations WHERE version = '944';
COMMIT;
