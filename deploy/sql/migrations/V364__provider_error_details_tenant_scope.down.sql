-- V364 down: remove provider error tenant/bucket index and bucket column.
-- Fail closed when rows collide under the legacy global identity; resolve or
-- archive production rows explicitly before retrying this rollback.
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
        RAISE EXCEPTION 'V364 down blocked: duplicate legacy provider_error_details identities require explicit operator resolution';
    END IF;
END
$$;

DROP INDEX IF EXISTS public.idx_provider_error_details_tenant_fingerprint;
ALTER TABLE IF EXISTS public.provider_error_details DROP COLUMN IF EXISTS aggregation_bucket;
COMMIT;
