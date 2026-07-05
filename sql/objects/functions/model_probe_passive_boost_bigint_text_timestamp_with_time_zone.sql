--
-- Name: model_probe_passive_boost(bigint, text, timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_passive_boost(p_credential_id bigint, p_raw_model_name text, p_now timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    recent_count INTEGER;
    new_retry TIMESTAMPTZ;
BEGIN
    SELECT COUNT(*) INTO recent_count
    FROM candidate_failure_logs
    WHERE credential_id = p_credential_id
      AND raw_model_name = p_raw_model_name
      AND ts > p_now - INTERVAL '5 minutes';

    IF recent_count >= 3 THEN
        new_retry := p_now + INTERVAL '30 seconds';
    ELSIF recent_count >= 2 THEN
        new_retry := p_now + INTERVAL '1 minute';
    ELSE
        RETURN;
    END IF;

    UPDATE model_probe_state mps
    SET next_retry_at = LEAST(COALESCE(mps.next_retry_at, new_retry), new_retry)
    WHERE mps.credential_id = p_credential_id
      AND mps.raw_model_name = p_raw_model_name
      AND COALESCE(mps.state, 'unknown') <> 'broken_confirmed';
END;
$$;

