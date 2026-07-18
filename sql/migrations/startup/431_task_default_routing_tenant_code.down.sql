-- 431_task_default_routing_tenant_code.down.sql
-- Purpose: Restore the previous numeric tenant scope representation.
-- Rollback: Execute only after all tenant codes are numeric values.

BEGIN;

DROP INDEX IF EXISTS public.uq_task_default_routing;

ALTER TABLE public.task_default_routing
    ALTER COLUMN tenant_id TYPE bigint
    USING CASE WHEN tenant_id IS NULL THEN NULL ELSE tenant_id::bigint END;

ALTER TABLE public.task_default_routing_audit
    ALTER COLUMN tenant_id TYPE bigint
    USING CASE WHEN tenant_id IS NULL THEN NULL ELSE tenant_id::bigint END;

CREATE UNIQUE INDEX uq_task_default_routing
    ON public.task_default_routing (task_type, profile, tier, COALESCE(tenant_id, 0));

COMMIT;
