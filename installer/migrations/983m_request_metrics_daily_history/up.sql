-- Depends on 983k. Closed-day report cells can be rebuilt from the projection.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE IF NOT EXISTS public.request_metrics_history_days (
    day date PRIMARY KEY REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS public.request_metrics_daily_history (
    report_date date NOT NULL REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    business_only boolean NOT NULL,
    dimension text NOT NULL CHECK (dimension IN ('total', 'provider', 'model')),
    group_key text NOT NULL,
    requests bigint NOT NULL,
    successes bigint NOT NULL,
    input_tokens bigint NOT NULL,
    output_tokens bigint NOT NULL,
    cache_read bigint NOT NULL,
    cache_write bigint NOT NULL,
    usd_cents numeric NOT NULL,
    credits bigint NOT NULL,
    tokens bigint NOT NULL,
    cache_covered_read bigint NOT NULL,
    cache_input bigint NOT NULL,
    cache_coverage bigint NOT NULL,
    usd_coverage bigint NOT NULL,
    -- Ascending 64-bit samples in PostgreSQL's int8send network representation.
    latency_samples bytea NOT NULL CHECK (octet_length(latency_samples) % 8 = 0),
    errors jsonb NOT NULL,
    PRIMARY KEY (report_date, business_only, dimension, group_key)
);

CREATE OR REPLACE FUNCTION public.invalidate_request_metrics_history_day()
RETURNS trigger LANGUAGE plpgsql SET search_path TO pg_catalog, public AS $$
BEGIN
    DELETE FROM public.request_metrics_history_days WHERE day=NEW.day;
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS invalidate_request_metrics_history_day ON public.request_metrics_daily_state;
CREATE TRIGGER invalidate_request_metrics_history_day
AFTER UPDATE OF dirty ON public.request_metrics_daily_state
FOR EACH ROW WHEN (NEW.dirty) EXECUTE FUNCTION public.invalidate_request_metrics_history_day();

INSERT INTO public.schema_migrations (version, description)
VALUES ('983m', 'rebuildable request history totals and exact latency samples')
ON CONFLICT (version) DO NOTHING;
COMMIT;
