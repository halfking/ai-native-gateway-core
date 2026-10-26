-- Migration 489: ensure durable credential probe queue exists in startup migrations.
-- Domain migrations 343/347/349 created this table, but 154 startup deploys did
-- not replay domain migrations, leaving ProbeQueueWorker with SQLSTATE 42P01.

CREATE TABLE IF NOT EXISTS public.credential_probe_queue (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    credential_id BIGINT NOT NULL,
    provider_id BIGINT,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    canonical_model TEXT,
    raw_model TEXT NOT NULL,
    outbound_model TEXT,
    probe_command TEXT NOT NULL,
    probe_mode TEXT NOT NULL DEFAULT 'single',
    priority SMALLINT NOT NULL DEFAULT 50,
    status TEXT NOT NULL DEFAULT 'ready',
    reason_code TEXT,
    reason_detail TEXT,
    attempt INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 1,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ,
    lease_token UUID,
    source TEXT NOT NULL DEFAULT 'request_failure',
    source_event_id TEXT,
    parent_request_id TEXT,
    dedup_key TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    result_http_status INT,
    result_latency_ms INT,
    result_body_preview TEXT,
    last_error TEXT,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '300 seconds'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT credential_probe_queue_status_check CHECK (status IN ('ready', 'running', 'success', 'failed', 'expired', 'cancelled')),
    CONSTRAINT credential_probe_queue_mode_check CHECK (probe_mode IN ('single', 'multi_round')),
    CONSTRAINT credential_probe_queue_attempt_check CHECK (attempt >= 0 AND max_attempts > 0),
    CONSTRAINT credential_probe_queue_source_check CHECK (source IN ('request_failure', 'periodic', 'external_async', 'admin', 'integrity_probe_planner'))
);

ALTER TABLE public.credential_probe_queue
    ADD COLUMN IF NOT EXISTS lease_token UUID,
    ADD COLUMN IF NOT EXISTS payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS parent_request_id TEXT,
    ADD COLUMN IF NOT EXISTS result_http_status INT,
    ADD COLUMN IF NOT EXISTS result_latency_ms INT,
    ADD COLUMN IF NOT EXISTS result_body_preview TEXT,
    ADD COLUMN IF NOT EXISTS last_error TEXT,
    ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS finished_at TIMESTAMPTZ;

ALTER TABLE public.credential_probe_queue
    DROP CONSTRAINT IF EXISTS credential_probe_queue_status_check;
ALTER TABLE public.credential_probe_queue
    ADD CONSTRAINT credential_probe_queue_status_check CHECK (
        status IN ('ready', 'running', 'success', 'failed', 'expired', 'cancelled')
    );

ALTER TABLE public.credential_probe_queue
    DROP CONSTRAINT IF EXISTS credential_probe_queue_probe_mode_check;
ALTER TABLE public.credential_probe_queue
    DROP CONSTRAINT IF EXISTS credential_probe_queue_mode_check;
ALTER TABLE public.credential_probe_queue
    ADD CONSTRAINT credential_probe_queue_mode_check CHECK (probe_mode IN ('single', 'multi_round'));

ALTER TABLE public.credential_probe_queue
    DROP CONSTRAINT IF EXISTS credential_probe_queue_source_check;
ALTER TABLE public.credential_probe_queue
    ADD CONSTRAINT credential_probe_queue_source_check CHECK (
        source IN ('request_failure', 'periodic', 'external_async', 'admin', 'integrity_probe_planner')
    );

CREATE UNIQUE INDEX IF NOT EXISTS uq_credential_probe_queue_dedup_active
    ON public.credential_probe_queue (dedup_key)
    WHERE status IN ('ready', 'running');

CREATE UNIQUE INDEX IF NOT EXISTS uq_credential_probe_queue_source_event
    ON public.credential_probe_queue (source_event_id)
    WHERE source_event_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_credential_probe_queue_claim
    ON public.credential_probe_queue (priority DESC, next_run_at, id)
    WHERE status = 'ready';

CREATE INDEX IF NOT EXISTS idx_credential_probe_queue_expiry
    ON public.credential_probe_queue (expires_at)
    WHERE status IN ('ready', 'running');

CREATE INDEX IF NOT EXISTS idx_credential_probe_queue_running_lease_token
    ON public.credential_probe_queue (id, lease_token)
    WHERE status = 'running';

COMMENT ON TABLE public.credential_probe_queue IS
    '488: durable credential+model probe tasks with TTL and worker leases; startup mirror of domain migrations 343/347/349.';
