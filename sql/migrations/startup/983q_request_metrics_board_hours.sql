-- Depends on 983k. Partial-day board summaries are derived from the projection.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE IF NOT EXISTS public.request_metrics_board_hour_days (
    day date PRIMARY KEY REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS public.request_metrics_board_hour_summary (
    LIKE public.request_metrics_daily_summary,
    bucket timestamptz NOT NULL,
    FOREIGN KEY (day) REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    PRIMARY KEY (day, bucket, tenant_id, traffic_class)
);

CREATE TABLE IF NOT EXISTS public.request_metrics_board_hour_masks (
    LIKE public.request_metrics_daily_session_masks,
    bucket timestamptz NOT NULL,
    FOREIGN KEY (day) REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    PRIMARY KEY (day, bucket, tenant_id, traffic_class, word)
);

CREATE OR REPLACE FUNCTION public.invalidate_request_metrics_board_hour_day()
RETURNS trigger LANGUAGE plpgsql SET search_path TO pg_catalog, public AS $$
BEGIN
    DELETE FROM public.request_metrics_board_hour_days WHERE day=NEW.day;
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS invalidate_request_metrics_board_hour_day ON public.request_metrics_daily_state;
CREATE TRIGGER invalidate_request_metrics_board_hour_day
AFTER UPDATE OF dirty ON public.request_metrics_daily_state
FOR EACH ROW WHEN (NEW.dirty) EXECUTE FUNCTION public.invalidate_request_metrics_board_hour_day();

INSERT INTO public.schema_migrations (version, description)
VALUES ('983q', 'rebuildable hourly request board summaries')
ON CONFLICT (version) DO NOTHING;
COMMIT;
