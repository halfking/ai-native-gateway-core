-- Down for 942: drop sales_supply_annotations.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.sales_supply_annotations;
DELETE FROM public.schema_migrations WHERE version = '942';
COMMIT;
