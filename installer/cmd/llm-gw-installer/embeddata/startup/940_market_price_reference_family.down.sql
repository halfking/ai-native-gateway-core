-- Down for 940: drop market intel family.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DROP TABLE IF EXISTS public.market_price_reference_sync;
DROP TABLE IF EXISTS public.supplier_market_position;
DROP TABLE IF EXISTS public.market_price_reference;
DELETE FROM public.schema_migrations WHERE version = '940';
COMMIT;
