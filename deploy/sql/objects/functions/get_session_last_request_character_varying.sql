--
-- Name: get_session_last_request(character varying); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.get_session_last_request(p_session_id character varying) RETURNS TABLE(request_id bigint, status character varying, user_message text, cached_response text, response_chunks integer, model character varying, provider_id integer, latency_ms integer, age_seconds integer)
    LANGUAGE plpgsql
    AS $$
BEGIN
    RETURN QUERY
    SELECT 
        last_request_id,
        last_request_status,
        last_request_user_message,
        last_response_cached,
        last_response_chunks,
        last_model,
        last_provider_id,
        last_latency_ms,
        EXTRACT(EPOCH FROM (NOW() - updated_at))::INT as age_seconds
    FROM session_last_requests
    WHERE session_id = p_session_id
      AND expires_at > NOW();
END;
$$;

