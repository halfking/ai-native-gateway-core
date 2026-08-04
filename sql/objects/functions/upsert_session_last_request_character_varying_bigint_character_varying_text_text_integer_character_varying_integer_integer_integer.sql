--
-- Name: upsert_session_last_request(character varying, bigint, character varying, text, text, integer, character varying, integer, integer, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.upsert_session_last_request(p_session_id character varying, p_request_id bigint, p_status character varying, p_user_message text, p_response_cached text, p_response_chunks integer, p_model character varying, p_provider_id integer, p_latency_ms integer, p_ttl_seconds integer DEFAULT 3600) RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO session_last_requests (
        session_id,
        last_request_id,
        last_request_status,
        last_request_user_message,
        last_response_cached,
        last_response_chunks,
        last_model,
        last_provider_id,
        last_latency_ms,
        expires_at
    ) VALUES (
        p_session_id,
        p_request_id,
        p_status,
        p_user_message,
        p_response_cached,
        p_response_chunks,
        p_model,
        p_provider_id,
        p_latency_ms,
        NOW() + (p_ttl_seconds || ' seconds')::INTERVAL
    )
    ON CONFLICT (session_id) 
    DO UPDATE SET
        last_request_id = EXCLUDED.last_request_id,
        last_request_status = EXCLUDED.last_request_status,
        last_request_user_message = EXCLUDED.last_request_user_message,
        last_response_cached = EXCLUDED.last_response_cached,
        last_response_chunks = EXCLUDED.last_response_chunks,
        last_model = EXCLUDED.last_model,
        last_provider_id = EXCLUDED.last_provider_id,
        last_latency_ms = EXCLUDED.last_latency_ms,
        expires_at = EXCLUDED.expires_at;
END;
$$;

