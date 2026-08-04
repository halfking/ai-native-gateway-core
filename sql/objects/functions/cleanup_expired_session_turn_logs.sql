--
-- Name: cleanup_expired_session_turn_logs(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.cleanup_expired_session_turn_logs() RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    deleted_count INT;
BEGIN
    DELETE FROM gateway.session_turn_logs
    WHERE expires_at < NOW();
    
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    
    IF deleted_count > 0 THEN
        RAISE NOTICE 'Cleaned up % expired session turn logs', deleted_count;
    END IF;
END;
$$;


--
-- Name: FUNCTION cleanup_expired_session_turn_logs(); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.cleanup_expired_session_turn_logs() IS 'Cleanup expired session turn logs (older than 24 hours).
     Should be called by bg worker or cron job every hour.
     Created: 2026-07-17, Migration 430';

