-- V364: tenant-scoped, replay-safe provider error aggregates.
BEGIN;

ALTER TABLE IF EXISTS public.provider_error_details
    ADD COLUMN IF NOT EXISTS aggregation_bucket TIMESTAMPTZ;

-- Fail closed rather than implicitly merging legacy production rows. Resolve
-- duplicate target identities explicitly and rerun this migration.
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
        RAISE EXCEPTION 'V364 blocked: duplicate provider_error_details target identities require explicit operator resolution';
    END IF;
END
$$;

DROP INDEX IF EXISTS public.idx_provider_error_details_fingerprint;
CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_error_details_tenant_fingerprint
ON public.provider_error_details (
    COALESCE(tenant_id, ''), provider_id, COALESCE(model_name, ''),
    COALESCE(endpoint, ''), error_type, COALESCE(error_code, ''),
    COALESCE(LEFT(error_message, 200), ''), COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
);

ALTER TABLE public.provider_error_details ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.provider_error_details FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS provider_error_details_tenant_isolation ON public.provider_error_details;
CREATE POLICY provider_error_details_tenant_isolation
    ON public.provider_error_details
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
        OR tenant_id = current_setting('app.current_tenant', true))
    WITH CHECK (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
        OR tenant_id = current_setting('app.current_tenant', true));

COMMIT;
