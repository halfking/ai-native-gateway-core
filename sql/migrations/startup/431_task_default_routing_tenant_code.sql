-- 431_task_default_routing_tenant_code.sql
-- Purpose: Align default routing tenant scope with tenants.code.
-- Idempotent: NO; execute once during the startup migration sequence.
-- Rollback: 431_task_default_routing_tenant_code.down.sql

BEGIN;

DROP INDEX IF EXISTS public.uq_task_default_routing;

ALTER TABLE public.task_default_routing
    ALTER COLUMN tenant_id TYPE varchar(64)
    USING CASE WHEN tenant_id IS NULL THEN NULL ELSE tenant_id::text END;

ALTER TABLE public.task_default_routing_audit
    ALTER COLUMN tenant_id TYPE varchar(64)
    USING CASE WHEN tenant_id IS NULL THEN NULL ELSE tenant_id::text END;

CREATE UNIQUE INDEX uq_task_default_routing
    ON public.task_default_routing (task_type, profile, tier, COALESCE(tenant_id, ''));

COMMIT;
