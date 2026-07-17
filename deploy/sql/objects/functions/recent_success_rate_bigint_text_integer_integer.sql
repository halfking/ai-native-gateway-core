--
-- Name: recent_success_rate(bigint, text, integer, integer); Type: FUNCTION; Schema: public; Owner: -
--
-- 2026-07-15: synced with migration 406. Reads request_logs_hot (where live
-- rows live; the request_logs partition parent only holds >7d-old promoted
-- rows, so a 3h window would otherwise return samples=0). Backed by the
-- idx_request_logs_hot_credential_model_ts expression index.

CREATE FUNCTION public.recent_success_rate(
    p_credential_id BIGINT,
    p_raw_model     TEXT,
    p_sample_n      INT DEFAULT 50,
    p_window_hours  INT DEFAULT 3
)
RETURNS TABLE(rate DOUBLE PRECISION, samples INT)
LANGUAGE sql STABLE
AS $$
    WITH recent AS (
        SELECT success
        FROM request_logs_hot
        WHERE credential_id = p_credential_id
          -- case-insensitive: request_logs_hot.outbound_model can differ in
          -- case from model_offers.raw_model_name (e.g. "MiniMax-M3" vs
          -- "minimax-m3"). Fall back to client_model when outbound is NULL.
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

COMMENT ON FUNCTION public.recent_success_rate(bigint, text, int, int) IS
    'Success fraction over the most recent p_sample_n rows within p_window_hours for a (credential, model) pair. Reads request_logs_hot (where live rows live; request_logs partition parent only holds >7d-old promoted rows). Returns samples=0/rate=NULL when empty. Used by loadCandidatesDB last-N gate and success-rate tiebreaker.';
