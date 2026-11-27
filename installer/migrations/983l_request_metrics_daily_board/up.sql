-- Depends on 983k. Board days are published under the same day lock.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE IF NOT EXISTS public.request_metrics_board_days (
    day date PRIMARY KEY REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS public.request_metrics_board_pies (
    day date NOT NULL REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    tenant_id text NOT NULL,
    all_tenants boolean NOT NULL,
    traffic_class text NOT NULL,
    dimension text NOT NULL,
    key text NOT NULL,
    requests bigint NOT NULL,
    tokens numeric NOT NULL,
    credits numeric NOT NULL,
    cost_usd numeric NOT NULL,
    PRIMARY KEY (day, all_tenants, tenant_id, traffic_class, dimension, key)
);
CREATE TABLE IF NOT EXISTS public.request_metrics_board_providers (
    day date NOT NULL REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    tenant_id text NOT NULL,
    all_tenants boolean NOT NULL,
    traffic_class text NOT NULL,
    provider_id bigint NOT NULL,
    provider_code text,
    provider_name text,
    request_count bigint NOT NULL,
    success_count bigint NOT NULL,
    prompt_tokens numeric NOT NULL,
    completion_tokens numeric NOT NULL,
    total_tokens numeric NOT NULL,
    credits numeric NOT NULL,
    total_cost_usd numeric NOT NULL,
    PRIMARY KEY (day, all_tenants, tenant_id, traffic_class, provider_id)
);
CREATE TABLE IF NOT EXISTS public.request_metrics_board_keys (
    day date NOT NULL REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    tenant_id text NOT NULL,
    all_tenants boolean NOT NULL,
    traffic_class text NOT NULL,
    word bigint NOT NULL,
    mask bigint NOT NULL,
    PRIMARY KEY (day, all_tenants, tenant_id, traffic_class, word)
);
CREATE TABLE IF NOT EXISTS public.request_metrics_board_series (
    day date NOT NULL REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    tenant_id text NOT NULL,
    all_tenants boolean NOT NULL,
    traffic_class text NOT NULL,
    minutes smallint NOT NULL CHECK (minutes IN (5,15,60,360)),
    bucket timestamptz NOT NULL,
    is_total boolean NOT NULL,
    model text NOT NULL,
    requests bigint NOT NULL,
    tokens numeric NOT NULL,
    credits numeric NOT NULL,
    cost_usd numeric NOT NULL,
    success bigint NOT NULL,
    latency_sum numeric,
    latency_count bigint NOT NULL,
    PRIMARY KEY (day, minutes, all_tenants, is_total, model, tenant_id, traffic_class, bucket)
);

-- Leave the derived rows available for asynchronous replacement. Revoking the
-- publication marker is small and commits together with projection changes.
CREATE OR REPLACE FUNCTION public.invalidate_request_metrics_board_day()
RETURNS trigger LANGUAGE plpgsql SET search_path TO pg_catalog, public AS $$
BEGIN
    DELETE FROM public.request_metrics_board_days WHERE day=NEW.day;
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS invalidate_request_metrics_board_day ON public.request_metrics_daily_state;
CREATE TRIGGER invalidate_request_metrics_board_day
AFTER UPDATE OF dirty ON public.request_metrics_daily_state
FOR EACH ROW WHEN (NEW.dirty) EXECUTE FUNCTION public.invalidate_request_metrics_board_day();

INSERT INTO public.schema_migrations (version, description)
VALUES ('983l', 'rebuildable request board distributions and trend buckets')
ON CONFLICT (version) DO NOTHING;
COMMIT;
