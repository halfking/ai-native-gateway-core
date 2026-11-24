\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

DROP TABLE IF EXISTS public.provider_invoice_lines;
DROP TABLE IF EXISTS public.provider_invoices;
DROP INDEX IF EXISTS public.idx_provider_call_attempts_provider_request;
DROP INDEX IF EXISTS public.idx_provider_call_attempts_account_time;
DELETE FROM public.schema_migrations WHERE version = '987';

COMMIT;
