-- Depends on 983j. Derived closed days can be dropped and rebuilt from the projection.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE IF NOT EXISTS public.request_metrics_daily_state (
    day date PRIMARY KEY,
    dirty boolean NOT NULL DEFAULT true,
    refreshed_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_request_metrics_daily_dirty
    ON public.request_metrics_daily_state (day DESC) WHERE dirty;

CREATE TABLE IF NOT EXISTS public.request_metrics_daily_summary (
    day date NOT NULL REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    tenant_id text NOT NULL,
    traffic_class text NOT NULL,
    models text[] NOT NULL,
    total_requests bigint NOT NULL,
    success_count bigint NOT NULL,
    failed_count bigint NOT NULL,
    timeout_count bigint NOT NULL,
    rate_limited_count bigint NOT NULL,
    cache_hit_requests bigint NOT NULL,
    cache_input_covered_requests bigint NOT NULL,
    cache_input_read_tokens numeric NOT NULL,
    cache_input_total_tokens numeric NOT NULL,
    measured_token_requests bigint NOT NULL,
    currency_evidence_requests bigint NOT NULL,
    usd_evidence_requests bigint NOT NULL,
    total_prompt_tokens numeric NOT NULL,
    total_completion_tokens numeric NOT NULL,
    total_tokens numeric NOT NULL,
    cache_read_tokens numeric NOT NULL,
    cache_write_tokens numeric NOT NULL,
    image_tokens numeric NOT NULL,
    audio_tokens numeric NOT NULL,
    video_tokens numeric NOT NULL,
    total_credits_charged numeric NOT NULL,
    total_cost_usd numeric NOT NULL,
    cost_rows_outside_usd bigint NOT NULL,
    latency_sum numeric,
    latency_count bigint NOT NULL,
    ttft_sum numeric,
    ttft_count bigint NOT NULL,
    first_request_at timestamptz NOT NULL,
    last_request_at timestamptz NOT NULL,
    PRIMARY KEY (day, tenant_id, traffic_class)
);

CREATE TABLE IF NOT EXISTS public.request_metrics_daily_session_codes (
    tenant_id text NOT NULL,
    session_id text NOT NULL,
    code bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (tenant_id, session_id),
    UNIQUE (code)
);
CREATE TABLE IF NOT EXISTS public.request_metrics_daily_session_masks (
    day date NOT NULL REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    tenant_id text NOT NULL,
    traffic_class text NOT NULL,
    word bigint NOT NULL,
    mask bigint NOT NULL,
    PRIMARY KEY (day, tenant_id, traffic_class, word)
);

CREATE OR REPLACE FUNCTION public.invalidate_request_metrics_daily()
RETURNS trigger LANGUAGE plpgsql SET search_path TO pg_catalog, public AS $$
DECLARE
    affected date[];
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT array_agg(DISTINCT (occurred_at AT TIME ZONE 'Asia/Shanghai')::date)
        INTO affected FROM new_rows WHERE source_fact_id IS NOT NULL AND isfinite(occurred_at);
    ELSIF TG_OP = 'DELETE' THEN
        SELECT array_agg(DISTINCT (occurred_at AT TIME ZONE 'Asia/Shanghai')::date)
        INTO affected FROM old_rows WHERE source_fact_id IS NOT NULL AND isfinite(occurred_at);
    ELSE
        SELECT array_agg(day ORDER BY day) INTO affected FROM (
            SELECT (occurred_at AT TIME ZONE 'Asia/Shanghai')::date AS day
            FROM old_rows WHERE source_fact_id IS NOT NULL AND isfinite(occurred_at)
            UNION
            SELECT (occurred_at AT TIME ZONE 'Asia/Shanghai')::date
            FROM new_rows WHERE source_fact_id IS NOT NULL AND isfinite(occurred_at)
        ) days;
    END IF;
    INSERT INTO public.request_metrics_daily_state AS s (day)
    SELECT day FROM unnest(affected) AS days(day) ORDER BY day
    ON CONFLICT (day) DO UPDATE SET dirty=true WHERE NOT s.dirty;
    RETURN NULL;
END;
$$;

-- A bulk clear also invalidates previously published days.
CREATE OR REPLACE FUNCTION public.invalidate_all_request_metrics_daily()
RETURNS trigger LANGUAGE plpgsql SET search_path TO pg_catalog, public AS $$
BEGIN
    UPDATE public.request_metrics_daily_state SET dirty=true WHERE NOT dirty;
    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS invalidate_request_metrics_daily_insert ON public.request_metrics;
CREATE TRIGGER invalidate_request_metrics_daily_insert AFTER INSERT ON public.request_metrics
REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.invalidate_request_metrics_daily();
DROP TRIGGER IF EXISTS invalidate_request_metrics_daily_update ON public.request_metrics;
CREATE TRIGGER invalidate_request_metrics_daily_update AFTER UPDATE ON public.request_metrics
REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.invalidate_request_metrics_daily();
DROP TRIGGER IF EXISTS invalidate_request_metrics_daily_delete ON public.request_metrics;
CREATE TRIGGER invalidate_request_metrics_daily_delete AFTER DELETE ON public.request_metrics
REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.invalidate_request_metrics_daily();
DROP TRIGGER IF EXISTS invalidate_request_metrics_daily_truncate ON public.request_metrics;
CREATE TRIGGER invalidate_request_metrics_daily_truncate AFTER TRUNCATE ON public.request_metrics
FOR EACH STATEMENT EXECUTE FUNCTION public.invalidate_all_request_metrics_daily();

-- Stage existing days from indexed endpoints. Each day is built by the worker.
INSERT INTO public.request_metrics_daily_state (day)
SELECT days.day::date FROM generate_series(
    (SELECT (min(occurred_at) AT TIME ZONE 'Asia/Shanghai')::date::timestamp
     FROM public.request_metrics_projection WHERE occurred_at > '-infinity' AND occurred_at < 'infinity'),
    (SELECT (max(occurred_at) AT TIME ZONE 'Asia/Shanghai')::date::timestamp
     FROM public.request_metrics_projection WHERE occurred_at > '-infinity' AND occurred_at < 'infinity'),
    interval '1 day') AS days(day)
ON CONFLICT DO NOTHING;

INSERT INTO public.schema_migrations (version, description)
VALUES ('983k', 'rebuildable request metrics daily summary and exact session membership')
ON CONFLICT (version) DO NOTHING;
COMMIT;
