-- Migration 620 rollback: restore the legacy provider error fingerprint index.
-- Environment: PostgreSQL 17+ after migration 620 has completed.
-- Dependency: this rollback can fail if tenant/bucket rows collide under the
-- legacy global fingerprint; resolve or archive those rows before execution.
-- Tenant isolation remains enabled; this rollback only removes the 620 index
-- and aggregation bucket column.
BEGIN;

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
