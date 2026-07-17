-- 428: Keep recent_success_rate probe exclusions compatible with existing hot tables.
-- The baseline schema and the quick-init fixtures may predate these optional fields.

\set ON_ERROR_STOP on

ALTER TABLE IF EXISTS public.request_logs_hot
    ADD COLUMN IF NOT EXISTS task_type text;

ALTER TABLE IF EXISTS public.request_logs_hot
    ADD COLUMN IF NOT EXISTS origin_stage varchar(32);

CREATE OR REPLACE FUNCTION public.recent_success_rate(
    p_credential_id bigint,
    p_raw_model text,
    p_sample_n integer DEFAULT 50,
    p_window_hours integer DEFAULT 3
) RETURNS TABLE(rate double precision, samples integer)
LANGUAGE sql
STABLE
AS $$
    WITH recent AS (
        SELECT success
        FROM public.request_logs_hot
        WHERE credential_id = p_credential_id
          AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
          AND ts > NOW() - (p_window_hours || ' hours')::interval
          AND COALESCE(task_type, '') <> 'probe_triggered'
          AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health')
          AND request_id NOT LIKE 'probe-%'
        ORDER BY ts DESC
        LIMIT p_sample_n
    )
    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
           COUNT(*)::int
    FROM recent;
$$;

COMMENT ON FUNCTION public.recent_success_rate(bigint, text, integer, integer) IS
    'Recent business-route success fraction excluding probe and self-check rows.';
