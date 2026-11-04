-- Down for 945: drop FK on sales_quotes, then the versions table + guard fn.
-- Assumes reverse-order application (946–948 down already ran).
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
ALTER TABLE public.sales_quotes
    DROP CONSTRAINT IF EXISTS sales_quotes_current_version_fk;
DROP TABLE IF EXISTS public.sales_quote_versions;
DROP FUNCTION IF EXISTS public.sales_quote_versions_immutable_guard();
DELETE FROM public.schema_migrations WHERE version = '945';
COMMIT;
