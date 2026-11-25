-- Depends on 983g. A separate publication keeps the 983g read contract valid
-- while an older gateway is still serving or refreshing its own day cells.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE IF NOT EXISTS public.request_metrics_tenant_history_days (
    day date PRIMARY KEY REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS public.request_metrics_daily_tenant_history (
    LIKE public.request_metrics_daily_history,
    FOREIGN KEY (report_date) REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    PRIMARY KEY (report_date, group_key),
    CHECK (business_only AND dimension = 'tenant'),
    CHECK (octet_length(latency_samples) % 8 = 0)
);

-- Person pages look up selected tenant/person pairs within an occurrence
-- window. The same index covers the count-only ranking query.
CREATE INDEX IF NOT EXISTS idx_request_metrics_projection_person_history
ON public.request_metrics_projection (tenant_id, (coalesce(nullif(person_key,''),'unknown')), occurred_at)
INCLUDE (person_key)
WHERE source_fact_id IS NOT NULL AND traffic_class='business';

CREATE OR REPLACE FUNCTION public.invalidate_request_metrics_tenant_history_day()
RETURNS trigger LANGUAGE plpgsql SET search_path TO pg_catalog, public AS $$
BEGIN
    DELETE FROM public.request_metrics_tenant_history_days WHERE day=NEW.day;
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS invalidate_request_metrics_tenant_history_day ON public.request_metrics_daily_state;
CREATE TRIGGER invalidate_request_metrics_tenant_history_day
AFTER UPDATE OF dirty ON public.request_metrics_daily_state
FOR EACH ROW WHEN (NEW.dirty) EXECUTE FUNCTION public.invalidate_request_metrics_tenant_history_day();

INSERT INTO public.schema_migrations (version, description)
VALUES ('983h', 'rebuildable tenant history with exact latency samples')
ON CONFLICT (version) DO NOTHING;
COMMIT;
