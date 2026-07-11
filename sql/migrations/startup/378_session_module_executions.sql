-- 378_session_module_executions.sql
--
-- Compatibility migration for databases where 360 was already applied.  It does
-- not recreate or replace the published schema.  moduleexec writes asynchronously
-- through a pooled connection and does not set app.current_tenant, so RLS on these
-- hot/archive tables would reject valid operational writes.

BEGIN;

DO $migration$
BEGIN
    IF to_regclass('public.session_module_executions_hot') IS NULL
       OR to_regclass('public.session_module_executions') IS NULL THEN
        RAISE EXCEPTION
            'migration 378 requires the tables created by migration 360; apply 360 first';
    END IF;
END
$migration$;

-- Remove only policies introduced by the withdrawn replacement migration.
DROP POLICY IF EXISTS tenant_isolation_session_module_executions_hot
    ON public.session_module_executions_hot;
DROP POLICY IF EXISTS tenant_isolation_session_module_executions
    ON public.session_module_executions;

-- Do not enable RLS here. The module executor uses a pool for asynchronous writes
-- and has no transaction-scoped app.current_tenant value to satisfy a tenant policy.
ALTER TABLE public.session_module_executions_hot DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.session_module_executions DISABLE ROW LEVEL SECURITY;

-- These constraints protect new writes without forcing a validation scan of
-- existing production rows during upgrade.
ALTER TABLE public.session_module_executions_hot
    DROP CONSTRAINT IF EXISTS chk_sme_hot_status;
ALTER TABLE public.session_module_executions_hot
    ADD CONSTRAINT chk_sme_hot_status
    CHECK (status IN ('pending', 'running', 'completed', 'failed', 'skipped')) NOT VALID;

ALTER TABLE public.session_module_executions
    DROP CONSTRAINT IF EXISTS chk_sme_status;
ALTER TABLE public.session_module_executions
    ADD CONSTRAINT chk_sme_status
    CHECK (status IN ('pending', 'running', 'completed', 'failed', 'skipped')) NOT VALID;

CREATE INDEX IF NOT EXISTS idx_sme_hot_cleanup
    ON public.session_module_executions_hot (created_at);

COMMIT;
