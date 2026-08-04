--
-- Name: system_health_status(integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.system_health_status(p_window_seconds integer DEFAULT 30) RETURNS TABLE(status text, success_rate numeric, sample_count bigint, failure_count bigint, last_check_at timestamp with time zone)
    LANGUAGE sql STABLE
    AS $$
    WITH win AS (
        SELECT
            COUNT(*)::bigint                AS n,
            COUNT(*) FILTER (WHERE success)::bigint AS ok,
            COUNT(*) FILTER (WHERE NOT success)::bigint AS fail
        FROM request_logs_hot
        WHERE ts >= now() - make_interval(secs => p_window_seconds)
    )
    SELECT
        CASE
            WHEN n = 0                                  THEN 'suspect'
            WHEN (ok::numeric / NULLIF(n,0)) >= 0.80    THEN 'ok'
            ELSE 'degraded'
        END                                            AS status,
        ROUND( (ok::numeric / NULLIF(n,0))::numeric, 4) AS success_rate,
        n                                              AS sample_count,
        fail                                           AS failure_count,
        now()                                          AS last_check_at
    FROM win;
$$;
CREATE TRIGGER approval_approvers_updated_at BEFORE UPDATE ON public.approval_approvers FOR EACH ROW EXECUTE FUNCTION public.update_approval_updated_at();

