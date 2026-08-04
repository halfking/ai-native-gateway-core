--
-- Name: cleanup_expired_session_requests(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.cleanup_expired_session_requests() RETURNS TABLE(deleted_count bigint)
    LANGUAGE plpgsql
    AS $$
DECLARE
    result BIGINT;
BEGIN
    DELETE FROM session_last_requests
    WHERE expires_at <= NOW();
    
    GET DIAGNOSTICS result = ROW_COUNT;
    
    RETURN QUERY SELECT result;
END;
$$;


--
-- Name: FUNCTION cleanup_expired_session_requests(); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.cleanup_expired_session_requests() IS '清理过期的会话请求缓存';

