--
-- Name: model_probe_expire_to_suspicious(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_expire_to_suspicious() RETURNS integer
    LANGUAGE plpgsql
    AS $$ DECLARE expired_count INTEGER; BEGIN WITH updated AS (UPDATE model_probe_state SET state = 'suspicious', marked_suspicious_at = NOW(), state_expires_at = NULL, next_retry_at = NOW() WHERE state IN ('available', 'unavailable') AND state_expires_at IS NOT NULL AND state_expires_at <= NOW() RETURNING 1) SELECT COUNT(*) INTO expired_count FROM updated; RETURN expired_count; END; $$;

