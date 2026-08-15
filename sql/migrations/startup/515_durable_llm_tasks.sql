-- Migration 515: durable LLM task persistence (SR-W3)
--
-- PostgreSQL is the source of truth for durable task state and terminal results.
-- PendingStore is an idempotent projection driven by durable_pending_outbox.
-- This migration is intentionally additive and safe to run repeatedly.

BEGIN;

CREATE TABLE IF NOT EXISTS public.durable_llm_tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    parent_request_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL,
    protocol TEXT NOT NULL,
    endpoint TEXT NOT NULL,

    request_snapshot_ciphertext TEXT NOT NULL,
    snapshot_version INTEGER NOT NULL,
    encryption_key_id TEXT NOT NULL,
    request_hash TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'accepted',
    error_kind TEXT,
    reason_code TEXT NOT NULL DEFAULT '',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMPTZ,
    deadline_at TIMESTAMPTZ NOT NULL,
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    fencing_token BIGINT NOT NULL DEFAULT 0,

    semantic_content_committed BOOLEAN NOT NULL DEFAULT FALSE,
    commit_state TEXT NOT NULL DEFAULT 'none',
    connection_attached BOOLEAN NOT NULL DEFAULT TRUE,
    last_disconnect_at TIMESTAMPTZ,

    result_ciphertext TEXT,
    result_object_ref TEXT,
    result_hash TEXT NOT NULL DEFAULT '',
    result_version BIGINT NOT NULL DEFAULT 0,
    content_type TEXT NOT NULL DEFAULT '',

    policy JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,

    CONSTRAINT durable_llm_tasks_status_check CHECK (status IN (
        'accepted', 'running', 'streaming', 'waiting_recovery',
        'retry_scheduled', 'completed', 'permanent_failed', 'expired',
        'cancelled', 'resume_safety_blocked'
    )),
    CONSTRAINT durable_llm_tasks_commit_state_check CHECK (commit_state IN (
        'none', 'metadata', 'content', 'tool_call', 'terminal'
    )),
    CONSTRAINT durable_llm_tasks_checkpoint_not_runnable_check CHECK (
        commit_state IN ('none', 'metadata')
        OR status NOT IN ('accepted', 'waiting_recovery', 'retry_scheduled')
    ),
    CONSTRAINT durable_llm_tasks_attempt_count_check CHECK (attempt_count >= 0),
    CONSTRAINT durable_llm_tasks_fencing_token_check CHECK (fencing_token >= 0),
    CONSTRAINT durable_llm_tasks_result_version_check CHECK (result_version >= 0),
    CONSTRAINT durable_llm_tasks_tenant_request_key UNIQUE (tenant_id, request_id),
    CONSTRAINT durable_llm_tasks_id_tenant_key UNIQUE (id, tenant_id)
);

-- A rerun against an early experimental table must repair the recovery gate
-- before making it mandatory.
UPDATE public.durable_llm_tasks
SET commit_state = 'none'
WHERE commit_state IS NULL;
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'durable_llm_tasks'
          AND column_name = 'tenant_id'
          AND data_type <> 'text'
    ) THEN
        ALTER TABLE public.durable_llm_tasks
            ALTER COLUMN tenant_id TYPE TEXT USING tenant_id::TEXT;
    END IF;
END;
$$;
ALTER TABLE public.durable_llm_tasks
    ALTER COLUMN commit_state SET DEFAULT 'none',
    ALTER COLUMN commit_state SET NOT NULL;

CREATE TABLE IF NOT EXISTS public.durable_llm_task_events (
    id BIGSERIAL PRIMARY KEY,
    task_id UUID NOT NULL,
    tenant_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    attempt_no INTEGER NOT NULL DEFAULT 0,
    event_type TEXT NOT NULL,
    from_status TEXT,
    to_status TEXT,
    reason_code TEXT,
    error_kind TEXT,
    fencing_token BIGINT NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT durable_llm_task_events_attempt_check CHECK (attempt_no >= 0),
    CONSTRAINT durable_llm_task_events_fencing_check CHECK (fencing_token >= 0),
    CONSTRAINT durable_llm_task_events_task_tenant_fkey
        FOREIGN KEY (task_id, tenant_id)
        REFERENCES public.durable_llm_tasks (id, tenant_id)
        ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS public.durable_pending_outbox (
    id BIGSERIAL PRIMARY KEY,
    task_id UUID NOT NULL,
    tenant_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    fencing_token BIGINT NOT NULL,
    result_version BIGINT NOT NULL,
    result_hash TEXT,
    projection_status TEXT NOT NULL,
    projection_payload JSONB NOT NULL,

    status TEXT NOT NULL DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at TIMESTAMPTZ,

    CONSTRAINT durable_pending_outbox_status_check CHECK (
        status IN ('pending', 'processing', 'delivered', 'failed')
    ),
    CONSTRAINT durable_pending_outbox_attempt_check CHECK (attempt_count >= 0),
    CONSTRAINT durable_pending_outbox_fencing_check CHECK (fencing_token >= 0),
    CONSTRAINT durable_pending_outbox_version_check CHECK (result_version >= 0),
    CONSTRAINT durable_pending_outbox_task_tenant_fkey
        FOREIGN KEY (task_id, tenant_id)
        REFERENCES public.durable_llm_tasks (id, tenant_id)
        ON DELETE CASCADE,
    CONSTRAINT durable_pending_outbox_task_version_key UNIQUE (task_id, result_version)
);

-- Scheduler and reaper paths.
CREATE INDEX IF NOT EXISTS idx_durable_llm_tasks_runnable
    ON public.durable_llm_tasks (status, next_retry_at, id)
    WHERE status IN ('waiting_recovery', 'retry_scheduled', 'running')
      AND commit_state IN ('none', 'metadata');
CREATE INDEX IF NOT EXISTS idx_durable_llm_tasks_lease_expiry
    ON public.durable_llm_tasks (lease_until, id)
    WHERE status = 'running' AND lease_until IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_durable_llm_tasks_deadline
    ON public.durable_llm_tasks (deadline_at, id)
    WHERE status NOT IN ('completed', 'permanent_failed', 'expired', 'cancelled', 'resume_safety_blocked');
CREATE INDEX IF NOT EXISTS idx_durable_llm_tasks_tenant_status
    ON public.durable_llm_tasks (tenant_id, status, next_retry_at);
CREATE INDEX IF NOT EXISTS idx_durable_llm_tasks_session_request
    ON public.durable_llm_tasks (session_id, request_id, updated_at DESC);

-- Append-only lifecycle lookup paths.
CREATE INDEX IF NOT EXISTS idx_durable_llm_task_events_task_created
    ON public.durable_llm_task_events (task_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_durable_llm_task_events_tenant_request
    ON public.durable_llm_task_events (tenant_id, request_id, created_at DESC, id DESC);

-- PendingStore projection claim and audit paths.
CREATE INDEX IF NOT EXISTS idx_durable_pending_outbox_ready
    ON public.durable_pending_outbox (next_attempt_at, id)
    WHERE status IN ('pending', 'failed');
CREATE INDEX IF NOT EXISTS idx_durable_pending_outbox_lease
    ON public.durable_pending_outbox (lease_until, id)
    WHERE status = 'processing' AND lease_until IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_durable_pending_outbox_tenant_created
    ON public.durable_pending_outbox (tenant_id, created_at DESC, id DESC);

-- Lifecycle events are immutable. Outbox rows remain updateable because delivery
-- workers advance their status and retry metadata in place.
CREATE OR REPLACE FUNCTION public.reject_durable_task_event_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'durable_llm_task_events is append-only';
END;
$$;
DROP TRIGGER IF EXISTS durable_llm_task_events_append_only ON public.durable_llm_task_events;
CREATE TRIGGER durable_llm_task_events_append_only
    BEFORE UPDATE OR DELETE ON public.durable_llm_task_events
    FOR EACH ROW EXECUTE FUNCTION public.reject_durable_task_event_mutation();

-- RLS is forced so table owners do not accidentally bypass tenant isolation.
-- Cross-tenant workers/admin paths must explicitly use the established service
-- GUCs (app.current_role=super_admin or app.bypass_rls=true).
ALTER TABLE public.durable_llm_tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.durable_llm_tasks FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS durable_llm_tasks_tenant_isolation ON public.durable_llm_tasks;
CREATE POLICY durable_llm_tasks_tenant_isolation ON public.durable_llm_tasks
    USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''));
DROP POLICY IF EXISTS durable_llm_tasks_controlled_bypass ON public.durable_llm_tasks;
CREATE POLICY durable_llm_tasks_controlled_bypass ON public.durable_llm_tasks
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

ALTER TABLE public.durable_llm_task_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.durable_llm_task_events FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS durable_llm_task_events_tenant_isolation ON public.durable_llm_task_events;
CREATE POLICY durable_llm_task_events_tenant_isolation ON public.durable_llm_task_events
    USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''));
DROP POLICY IF EXISTS durable_llm_task_events_controlled_bypass ON public.durable_llm_task_events;
CREATE POLICY durable_llm_task_events_controlled_bypass ON public.durable_llm_task_events
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

ALTER TABLE public.durable_pending_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.durable_pending_outbox FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS durable_pending_outbox_tenant_isolation ON public.durable_pending_outbox;
CREATE POLICY durable_pending_outbox_tenant_isolation ON public.durable_pending_outbox
    USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''));
DROP POLICY IF EXISTS durable_pending_outbox_controlled_bypass ON public.durable_pending_outbox;
CREATE POLICY durable_pending_outbox_controlled_bypass ON public.durable_pending_outbox
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMIT;
