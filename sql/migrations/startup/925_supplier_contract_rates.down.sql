\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';
DROP TABLE IF EXISTS public.supplier_contract_rates;
DELETE FROM public.schema_migrations WHERE version = '925';
COMMIT;
