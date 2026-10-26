-- Migration 490: complete credential_probe_queue runtime columns after 489 backfill.
-- 488 may already have been applied with the early queue shape; ProbeQueueWorker
-- requires these audit/result columns and the current status/mode/source enums.

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

CREATE INDEX IF NOT EXISTS idx_credential_probe_queue_running_lease_token
    ON public.credential_probe_queue (id, lease_token)
    WHERE status = 'running';

COMMENT ON TABLE public.credential_probe_queue IS
    '490: durable credential+model probe tasks with runtime audit/result columns and worker leases.';
