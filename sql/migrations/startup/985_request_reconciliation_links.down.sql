\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

DROP TABLE IF EXISTS public.request_reconciliation_links;
DELETE FROM public.schema_migrations WHERE version = '984';
COMMIT;
