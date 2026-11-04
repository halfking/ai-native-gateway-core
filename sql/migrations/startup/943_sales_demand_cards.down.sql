-- Down for 943: drop sales_demand_cards.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.sales_demand_cards;
DELETE FROM public.schema_migrations WHERE version = '943';
COMMIT;
