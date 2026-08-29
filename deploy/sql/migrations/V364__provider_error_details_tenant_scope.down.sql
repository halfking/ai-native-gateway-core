-- V364 down: remove provider error tenant/bucket index and bucket column.
BEGIN;
DROP INDEX IF EXISTS public.idx_provider_error_details_tenant_fingerprint;
ALTER TABLE IF EXISTS public.provider_error_details DROP COLUMN IF EXISTS aggregation_bucket;
COMMIT;
