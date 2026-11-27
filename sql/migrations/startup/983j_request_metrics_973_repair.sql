-- Depends on 973. Reuse its physical projection and keep both installed queues
-- compatible during a rolling upgrade. The repair cursor is independently rebuilt.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

SELECT singleton FROM public.request_metrics_projection_state WHERE singleton FOR UPDATE;

DO $$
DECLARE
    widen text;
BEGIN
    SELECT string_agg(format('ALTER COLUMN %I TYPE bigint', attname), ', ' ORDER BY attnum)
    INTO widen
    FROM pg_attribute
    WHERE attrelid = 'public.request_metrics'::regclass AND NOT attisdropped
      AND attname IN ('prompt_tokens','completion_tokens','cached_tokens','reasoning_tokens',
                      'total_tokens','image_tokens','audio_tokens','video_tokens','latency_ms','ttft_ms')
      AND atttypid <> 'bigint'::regtype;

    IF widen IS NOT NULL THEN
        EXECUTE 'ALTER TABLE public.request_metrics ' || widen;
    END IF;
END
$$;

DO $$
DECLARE
    columns text;
BEGIN
    SELECT string_agg(format('m.%I', attname), ', ' ORDER BY attnum)
    INTO columns
    FROM pg_attribute
    WHERE attrelid = 'public.request_metrics'::regclass
      AND attnum > 0 AND NOT attisdropped AND attname NOT IN ('request_id', 'fact_request_id');

    EXECUTE 'CREATE OR REPLACE VIEW public.request_metrics_projection AS
        SELECT m.fact_request_id AS request_id, ' || columns ||
        ' FROM public.request_metrics m WHERE m.source_fact_id IS NOT NULL';
END
$$;

CREATE INDEX IF NOT EXISTS idx_request_metrics_projection_provider
    ON public.request_metrics (provider_id, occurred_at) WHERE source_fact_id IS NOT NULL AND provider_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_request_metrics_projection_session
    ON public.request_metrics (tenant_id, session_id, occurred_at) WHERE source_fact_id IS NOT NULL AND session_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS public.request_metrics_refresh_pending (
    kind text NOT NULL CHECK (kind IN ('fact', 'dimension')),
    tenant_id text NOT NULL,
    request_id text NOT NULL,
    occurred_at timestamptz NOT NULL,
    enqueued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (kind, tenant_id, request_id, occurred_at)
);
CREATE INDEX IF NOT EXISTS idx_request_metrics_refresh_pending_age
    ON public.request_metrics_refresh_pending (enqueued_at, kind, tenant_id, request_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_request_metrics_refresh_pending_time
    ON public.request_metrics_refresh_pending (occurred_at, tenant_id);

CREATE TABLE IF NOT EXISTS public.request_metrics_repair_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    cursor_fact_id bigint NOT NULL DEFAULT 0,
    cursor_occurred_at timestamptz NOT NULL DEFAULT '-infinity',
    backfill_complete boolean NOT NULL DEFAULT false,
    updated_at timestamptz
);

INSERT INTO public.request_metrics_repair_state (singleton) VALUES (true) ON CONFLICT DO NOTHING;

-- Readiness reports outstanding events across both worker queues. The legacy
-- schema allows NULL tenant IDs on old pending rows.
CREATE OR REPLACE VIEW public.request_metrics_pending_events AS
SELECT tenant_id, occurred_at, enqueued_at FROM public.request_metrics_pending
UNION ALL
SELECT tenant_id, occurred_at, enqueued_at FROM public.request_metrics_refresh_pending;

CREATE OR REPLACE FUNCTION public.enqueue_request_metrics_refresh_fact()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        INSERT INTO public.request_metrics_refresh_pending (kind, tenant_id, request_id, occurred_at)
        VALUES ('fact', OLD.tenant_id, OLD.request_id, OLD.occurred_at)
        ON CONFLICT (kind, tenant_id, request_id, occurred_at) DO UPDATE
        SET enqueued_at = request_metrics_refresh_pending.enqueued_at;
    END IF;
    IF TG_OP = 'INSERT' OR (TG_OP = 'UPDATE' AND
            (NEW.tenant_id, NEW.request_id, NEW.occurred_at) IS DISTINCT FROM
            (OLD.tenant_id, OLD.request_id, OLD.occurred_at)) THEN
        INSERT INTO public.request_metrics_refresh_pending (kind, tenant_id, request_id, occurred_at)
        VALUES ('fact', NEW.tenant_id, NEW.request_id, NEW.occurred_at)
        ON CONFLICT (kind, tenant_id, request_id, occurred_at) DO UPDATE
        SET enqueued_at = request_metrics_refresh_pending.enqueued_at;
    END IF;
    RETURN NULL;
END
$$;

CREATE OR REPLACE FUNCTION public.enqueue_request_metrics_dimension()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO public.request_metrics_refresh_pending (kind, tenant_id, request_id, occurred_at)
    VALUES ('dimension', NEW.tenant_id, NEW.request_id, NEW.ts)
    ON CONFLICT (kind, tenant_id, request_id, occurred_at) DO UPDATE
    SET enqueued_at = request_metrics_refresh_pending.enqueued_at;
    RETURN NULL;
END
$$;

DROP TRIGGER IF EXISTS enqueue_request_metrics_refresh_fact ON public.usage_facts;
CREATE TRIGGER enqueue_request_metrics_refresh_fact
AFTER INSERT OR UPDATE OR DELETE ON public.usage_facts
FOR EACH ROW EXECUTE FUNCTION public.enqueue_request_metrics_refresh_fact();

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
VALUES ('983j', 'compatible 973 projection repair and durable refresh events')
ON CONFLICT (version) DO NOTHING;
COMMIT;
