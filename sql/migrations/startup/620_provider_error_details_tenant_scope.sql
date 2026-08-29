-- Migration 620: make provider error aggregates tenant-scoped and replay-safe.
BEGIN;

ALTER TABLE public.provider_error_details
    ADD COLUMN IF NOT EXISTS aggregation_bucket TIMESTAMPTZ;

-- Replace the global fingerprint with a tenant-scoped identity. Existing rows
-- are retained; duplicate legacy fingerprints are merged before the constraint.
DROP INDEX IF EXISTS public.idx_provider_error_details_fingerprint;
CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_error_details_tenant_fingerprint
ON public.provider_error_details (
    COALESCE(tenant_id, ''), provider_id, COALESCE(model_name, ''),
    COALESCE(endpoint, ''), error_type, COALESCE(error_code, ''),
    COALESCE(LEFT(error_message, 200), ''), aggregation_bucket
);

ALTER TABLE public.provider_error_details ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.provider_error_details FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS provider_error_details_tenant_isolation ON public.provider_error_details;
CREATE POLICY provider_error_details_tenant_isolation
    ON public.provider_error_details
    USING (
        current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
        OR tenant_id = current_setting('app.current_tenant', true)
    )
    WITH CHECK (
        current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
        OR tenant_id = current_setting('app.current_tenant', true)
    );

COMMIT;
