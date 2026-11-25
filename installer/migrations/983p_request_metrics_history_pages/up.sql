-- Depends on 983m. Publish single-day detail cells independently of older readers.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE IF NOT EXISTS public.request_metrics_history_page_days (
    day date PRIMARY KEY REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS public.request_metrics_daily_history_page_cells (
    LIKE public.request_metrics_daily_history,
    tenant_id text NOT NULL,
    latency_ranks double precision[],
    FOREIGN KEY (report_date) REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    PRIMARY KEY (report_date, business_only, dimension, tenant_id, group_key),
    CHECK (dimension IN ('tenant', 'person', 'credential', 'apikey', 'provider', 'model')),
    CHECK (dimension = 'person' OR tenant_id = ''),
    CHECK (octet_length(latency_samples) % 8 = 0),
    CHECK (CASE WHEN octet_length(latency_samples) > 0 THEN latency_ranks IS NOT NULL AND cardinality(latency_ranks) = 2
           ELSE latency_ranks IS NULL END)
);

CREATE OR REPLACE FUNCTION public.invalidate_request_metrics_history_page_day()
RETURNS trigger LANGUAGE plpgsql SET search_path TO pg_catalog, public AS $$
BEGIN
    DELETE FROM public.request_metrics_history_page_days WHERE day=NEW.day;
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS invalidate_request_metrics_history_page_day ON public.request_metrics_daily_state;
CREATE TRIGGER invalidate_request_metrics_history_page_day
AFTER UPDATE OF dirty ON public.request_metrics_daily_state
FOR EACH ROW WHEN (NEW.dirty) EXECUTE FUNCTION public.invalidate_request_metrics_history_page_day();

INSERT INTO public.schema_migrations (version, description)
VALUES ('983p', 'rebuildable single-day request history detail cells')
ON CONFLICT (version) DO NOTHING;
COMMIT;
