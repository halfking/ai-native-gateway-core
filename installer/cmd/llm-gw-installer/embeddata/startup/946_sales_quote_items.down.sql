-- Down for 946: drop sales_quote_items.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.sales_quote_items;
DELETE FROM public.schema_migrations WHERE version = '946';
COMMIT;
