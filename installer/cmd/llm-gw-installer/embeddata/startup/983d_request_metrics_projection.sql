-- Dependencies: 537 (usage facts), 983/983b (legacy metrics), and the context/turn writers.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- Give the canonical projection its own storage and identity. Old gateway slots
-- read an unfiltered watermark from public.request_metrics.
CREATE TABLE IF NOT EXISTS public.request_metrics_projection (
    LIKE public.request_metrics INCLUDING DEFAULTS,
    PRIMARY KEY (tenant_id, request_id, occurred_at)
);

ALTER TABLE public.request_metrics_projection
    ALTER COLUMN prompt_tokens TYPE bigint,
    ALTER COLUMN completion_tokens TYPE bigint,
    ALTER COLUMN cached_tokens TYPE bigint,
    ALTER COLUMN reasoning_tokens TYPE bigint,
    ALTER COLUMN total_tokens TYPE bigint,
    ALTER COLUMN image_tokens TYPE bigint,
    ALTER COLUMN audio_tokens TYPE bigint,
    ALTER COLUMN video_tokens TYPE bigint,
    ALTER COLUMN latency_ms TYPE bigint,
    ALTER COLUMN ttft_ms TYPE bigint,
    ADD COLUMN IF NOT EXISTS raw_model_name text,
    ADD COLUMN IF NOT EXISTS credential_id bigint,
    ADD COLUMN IF NOT EXISTS credential_label text,
    ADD COLUMN IF NOT EXISTS provider_code text,
    ADD COLUMN IF NOT EXISTS provider_name text,
    ADD COLUMN IF NOT EXISTS client_id text,
    ADD COLUMN IF NOT EXISTS agent_name text,
    ADD COLUMN IF NOT EXISTS client_ip inet,
    ADD COLUMN IF NOT EXISTS identity_hash text,
    ADD COLUMN IF NOT EXISTS client_model text,
    ADD COLUMN IF NOT EXISTS task_type text,
    ADD COLUMN IF NOT EXISTS work_type text,
    ADD COLUMN IF NOT EXISTS session_intent text,
    ADD COLUMN IF NOT EXISTS compression_applied boolean,
    ADD COLUMN IF NOT EXISTS owner_user text,
    ADD COLUMN IF NOT EXISTS api_key_owner_user text,
    ADD COLUMN IF NOT EXISTS client_cancelled boolean,
    ADD COLUMN IF NOT EXISTS api_key_prefix text,
    ADD COLUMN IF NOT EXISTS application_code text,
    ADD COLUMN IF NOT EXISTS person_key text,
    ADD COLUMN IF NOT EXISTS cache_write_tokens bigint,
    ADD COLUMN IF NOT EXISTS cache_input_tokens bigint,
    ADD COLUMN IF NOT EXISTS cost_amount numeric(20,8),
    ADD COLUMN IF NOT EXISTS cost_currency text,
    ADD COLUMN IF NOT EXISTS usage_source text,
    ADD COLUMN IF NOT EXISTS source_revision integer,
    ADD COLUMN IF NOT EXISTS source_finalized_at timestamptz,
    ADD COLUMN IF NOT EXISTS source_fact_id bigint;

CREATE INDEX IF NOT EXISTS idx_request_metrics_projection_time
    ON public.request_metrics_projection (occurred_at);
CREATE INDEX IF NOT EXISTS idx_request_metrics_projection_provider
    ON public.request_metrics_projection (provider_id, occurred_at) WHERE provider_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_metrics_projection_session
    ON public.request_metrics_projection (tenant_id, session_id, occurred_at) WHERE session_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS public.request_metrics_pending (
    kind text NOT NULL CHECK (kind IN ('fact', 'dimension')),
    tenant_id text NOT NULL,
    request_id text NOT NULL,
    occurred_at timestamptz NOT NULL,
    enqueued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (kind, tenant_id, request_id, occurred_at)
);
CREATE INDEX IF NOT EXISTS idx_request_metrics_pending_age
    ON public.request_metrics_pending (enqueued_at, kind, tenant_id, request_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_request_metrics_pending_time
    ON public.request_metrics_pending (occurred_at, tenant_id);

CREATE TABLE IF NOT EXISTS public.request_metrics_projection_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    cursor_fact_id bigint NOT NULL DEFAULT 0,
    cursor_occurred_at timestamptz NOT NULL DEFAULT '-infinity',
    backfill_complete boolean NOT NULL DEFAULT false,
    updated_at timestamptz
);
INSERT INTO public.request_metrics_projection_state (singleton) VALUES (true) ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION public.enqueue_request_metrics_fact()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        INSERT INTO public.request_metrics_pending (kind, tenant_id, request_id, occurred_at)
        VALUES ('fact', OLD.tenant_id, OLD.request_id, OLD.occurred_at)
        ON CONFLICT (kind, tenant_id, request_id, occurred_at) DO UPDATE
        SET enqueued_at = request_metrics_pending.enqueued_at;
    END IF;
    IF TG_OP = 'INSERT' OR (TG_OP = 'UPDATE' AND
            (NEW.tenant_id, NEW.request_id, NEW.occurred_at) IS DISTINCT FROM
            (OLD.tenant_id, OLD.request_id, OLD.occurred_at)) THEN
        INSERT INTO public.request_metrics_pending (kind, tenant_id, request_id, occurred_at)
        VALUES ('fact', NEW.tenant_id, NEW.request_id, NEW.occurred_at)
        ON CONFLICT (kind, tenant_id, request_id, occurred_at) DO UPDATE
        SET enqueued_at = request_metrics_pending.enqueued_at;
    END IF;
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION public.enqueue_request_metrics_dimension()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO public.request_metrics_pending (kind, tenant_id, request_id, occurred_at)
    VALUES ('dimension', NEW.tenant_id, NEW.request_id, NEW.ts)
    ON CONFLICT (kind, tenant_id, request_id, occurred_at) DO UPDATE
    SET enqueued_at = request_metrics_pending.enqueued_at;
    RETURN NULL;
END
$$;

DROP TRIGGER IF EXISTS enqueue_request_metrics_fact ON public.usage_facts;
CREATE TRIGGER enqueue_request_metrics_fact
AFTER INSERT OR UPDATE OR DELETE ON public.usage_facts
FOR EACH ROW EXECUTE FUNCTION public.enqueue_request_metrics_fact();

DROP TRIGGER IF EXISTS enqueue_request_metrics_context ON public.request_context_attrs;
CREATE TRIGGER enqueue_request_metrics_context
AFTER INSERT OR UPDATE ON public.request_context_attrs
FOR EACH ROW WHEN (NEW.request_id IS NOT NULL AND NEW.tenant_id IS NOT NULL AND NEW.ts IS NOT NULL)
EXECUTE FUNCTION public.enqueue_request_metrics_dimension();

DROP TRIGGER IF EXISTS enqueue_request_metrics_turn ON public.session_turns_hot;
CREATE TRIGGER enqueue_request_metrics_turn
AFTER INSERT OR UPDATE ON public.session_turns_hot
FOR EACH ROW WHEN (NEW.request_id IS NOT NULL AND NEW.request_id <> '' AND NEW.tenant_id IS NOT NULL)
EXECUTE FUNCTION public.enqueue_request_metrics_dimension();

INSERT INTO public.schema_migrations (version, description)
VALUES ('983d', 'terminal request metrics projection and durable refresh queue')
ON CONFLICT (version) DO NOTHING;
COMMIT;
