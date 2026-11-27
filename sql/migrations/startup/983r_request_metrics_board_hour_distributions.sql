-- Depends on 983l and 983q. Derived hourly distributions share the board's day lock.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE IF NOT EXISTS public.request_metrics_board_hour_distribution_days (
    day date PRIMARY KEY REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS public.request_metrics_board_hour_pies (
    LIKE public.request_metrics_board_pies,
    bucket timestamptz NOT NULL,
    FOREIGN KEY (day) REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    PRIMARY KEY (day, bucket, all_tenants, tenant_id, traffic_class, dimension, key)
);
CREATE TABLE IF NOT EXISTS public.request_metrics_board_hour_providers (
    LIKE public.request_metrics_board_providers,
    bucket timestamptz NOT NULL,
    FOREIGN KEY (day) REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    PRIMARY KEY (day, bucket, all_tenants, tenant_id, traffic_class, provider_id)
);
CREATE TABLE IF NOT EXISTS public.request_metrics_board_hour_keys (
    LIKE public.request_metrics_board_keys,
    bucket timestamptz NOT NULL,
    FOREIGN KEY (day) REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    PRIMARY KEY (day, bucket, all_tenants, tenant_id, traffic_class, word)
);

CREATE OR REPLACE FUNCTION public.invalidate_request_metrics_board_hour_distribution_day()
RETURNS trigger LANGUAGE plpgsql SET search_path TO pg_catalog, public AS $$
BEGIN
    DELETE FROM public.request_metrics_board_hour_distribution_days WHERE day=NEW.day;
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS invalidate_request_metrics_board_hour_distribution_day ON public.request_metrics_daily_state;
CREATE TRIGGER invalidate_request_metrics_board_hour_distribution_day
AFTER UPDATE OF dirty ON public.request_metrics_daily_state
FOR EACH ROW WHEN (NEW.dirty) EXECUTE FUNCTION public.invalidate_request_metrics_board_hour_distribution_day();

INSERT INTO public.schema_migrations (version, description)
VALUES ('983r', 'rebuildable hourly request board distributions')
ON CONFLICT (version) DO NOTHING;
COMMIT;
