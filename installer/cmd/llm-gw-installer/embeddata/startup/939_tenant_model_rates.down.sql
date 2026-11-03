-- Down for 939: drop tenant_model_rates.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.tenant_model_rates;
DELETE FROM public.schema_migrations WHERE version = '939';
COMMIT;
