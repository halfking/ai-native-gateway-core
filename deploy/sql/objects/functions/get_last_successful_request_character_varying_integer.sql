--
-- Name: get_last_successful_request(character varying, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.get_last_successful_request(p_session_id character varying, p_minutes integer DEFAULT 60) RETURNS TABLE(request_id bigint, created_at timestamp with time zone, latency_ms integer, response_text text)
    LANGUAGE plpgsql
    AS $$
BEGIN
    RETURN QUERY
    SELECT 
        id as request_id,
        r.created_at,
        r.latency_ms,
        r.response_text
    FROM request_logs r
    WHERE r.session_id = p_session_id
      AND r.success = true
      AND r.created_at > NOW() - (p_minutes || ' minutes')::INTERVAL
    ORDER BY r.created_at DESC
    LIMIT 1;
END;
$$;

