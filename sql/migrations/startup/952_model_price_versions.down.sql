-- Down for 952: drop model_price_versions.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.model_price_versions;
DELETE FROM public.schema_migrations WHERE version = '952';
COMMIT;
