-- Migration 639 rollback: restore the 620 tenant-scoped fingerprint index
-- (no credential dimension) and drop the credential_id column.
-- Fails closed when per-credential rows collide under the 620 identity
-- (same convention as 620 down): resolve or archive rows explicitly first.
BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM public.provider_error_details
        GROUP BY COALESCE(tenant_id, ''), provider_id, COALESCE(model_name, ''),
                 COALESCE(endpoint, ''), error_type, COALESCE(error_code, ''),
                 COALESCE(LEFT(error_message, 200), ''),
                 COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'migration 639 down blocked: duplicate 620-identity provider_error_details rows require explicit operator resolution';
    END IF;
END
$$;

DROP INDEX IF EXISTS public.idx_ped_tenant_credential;
DROP INDEX IF EXISTS public.idx_provider_error_details_tenant_cred_fingerprint;
ALTER TABLE public.provider_error_details
    DROP COLUMN IF EXISTS credential_id;

CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_error_details_tenant_fingerprint
ON public.provider_error_details (
    COALESCE(tenant_id, ''), provider_id, COALESCE(model_name, ''),
    COALESCE(endpoint, ''), error_type, COALESCE(error_code, ''),
    COALESCE(LEFT(error_message, 200), ''), COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
);

COMMIT;
