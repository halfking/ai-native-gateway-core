-- Migration 620 rollback: restore the legacy provider error fingerprint index.
-- Environment: PostgreSQL 17+ after migration 620 has completed.
-- Fail closed when tenant/bucket rows collide under the legacy global identity;
-- resolve or archive production rows explicitly before retrying this rollback.
-- Tenant isolation remains enabled; this rollback only removes the 620 index
-- and aggregation bucket column.
BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.provider_error_details
        GROUP BY provider_id, COALESCE(model_name, ''), COALESCE(endpoint, ''),
                 error_type, COALESCE(error_code, ''),
                 COALESCE(LEFT(error_message, 200), '')
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'migration 620 down blocked: duplicate legacy provider_error_details identities require explicit operator resolution';
    END IF;
END
$$;

DROP INDEX IF EXISTS public.idx_provider_error_details_tenant_fingerprint;
ALTER TABLE public.provider_error_details
    DROP COLUMN IF EXISTS aggregation_bucket;

CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_error_details_fingerprint
ON public.provider_error_details (
    provider_id,
    COALESCE(model_name, ''),
    COALESCE(endpoint, ''),
    error_type,
    COALESCE(error_code, ''),
    COALESCE(LEFT(error_message, 200), '')
);

COMMIT;
