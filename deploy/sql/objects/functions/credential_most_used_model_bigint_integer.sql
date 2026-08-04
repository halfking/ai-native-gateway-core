--
-- Name: credential_most_used_model(bigint, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.credential_most_used_model(p_credential_id bigint, p_window_hours integer DEFAULT 24) RETURNS TABLE(raw_model_name text, call_count bigint)
    LANGUAGE sql STABLE
    AS $$
    SELECT pm.raw_model_name, COUNT(*) AS call_count
    FROM request_logs_hot rl
    JOIN provider_models pm ON pm.id = rl.canonical_id
    WHERE rl.credential_id = p_credential_id
      AND rl.ts >= now() - make_interval(hours => p_window_hours)
      AND rl.success = TRUE
    GROUP BY pm.raw_model_name
    ORDER BY call_count DESC, pm.raw_model_name ASC
    LIMIT 1;
$$;

