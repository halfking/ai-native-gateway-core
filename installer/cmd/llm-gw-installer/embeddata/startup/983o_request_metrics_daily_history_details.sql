-- Publish rebuildable tenant models and person counts independently of 983m/983n.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

CREATE TABLE IF NOT EXISTS public.request_metrics_history_detail_days (
    day date PRIMARY KEY REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS public.request_metrics_person_rank_codes (
    code bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL,
    person text NOT NULL,
    UNIQUE (tenant_id, person)
);

CREATE TABLE IF NOT EXISTS public.request_metrics_person_rank_packs (
    report_date date NOT NULL REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    tenant_id text NOT NULL,
    -- Each record is a positive dictionary code and count, both big-endian int64.
    counts bytea NOT NULL CHECK (octet_length(counts) > 0 AND octet_length(counts) % 16 = 0),
    PRIMARY KEY (report_date, tenant_id)
);

CREATE TABLE IF NOT EXISTS public.request_metrics_daily_tenant_model_history (
    LIKE public.request_metrics_daily_history,
    tenant_id text NOT NULL,
    FOREIGN KEY (report_date) REFERENCES public.request_metrics_daily_state (day) ON DELETE CASCADE,
    PRIMARY KEY (tenant_id, report_date, group_key),
    CHECK (business_only AND dimension = 'model'),
    CHECK (octet_length(latency_samples) % 8 = 0)
);

CREATE OR REPLACE FUNCTION public.invalidate_request_metrics_history_detail_day()
RETURNS trigger LANGUAGE plpgsql SET search_path TO pg_catalog, public AS $$
BEGIN
    DELETE FROM public.request_metrics_history_detail_days WHERE day=NEW.day;
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS invalidate_request_metrics_history_detail_day ON public.request_metrics_daily_state;
CREATE TRIGGER invalidate_request_metrics_history_detail_day
AFTER UPDATE OF dirty ON public.request_metrics_daily_state
FOR EACH ROW WHEN (NEW.dirty) EXECUTE FUNCTION public.invalidate_request_metrics_history_detail_day();

INSERT INTO public.schema_migrations (version, description)
VALUES ('983o', 'rebuildable tenant model cells and packed person counts')
ON CONFLICT (version) DO NOTHING;
COMMIT;
