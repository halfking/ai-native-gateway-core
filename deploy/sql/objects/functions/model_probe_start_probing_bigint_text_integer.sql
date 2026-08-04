--
-- Name: model_probe_start_probing(bigint, text, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_start_probing(p_credential_id bigint, p_raw_model_name text, p_max_credential_concurrency integer DEFAULT 2) RETURNS boolean
    LANGUAGE plpgsql
    AS $$ DECLARE current_concurrency INTEGER; can_probe BOOLEAN := FALSE; BEGIN SELECT model_probe_credential_concurrency(p_credential_id) INTO current_concurrency; IF current_concurrency >= p_max_credential_concurrency THEN RETURN FALSE; END IF; WITH updated AS (UPDATE model_probe_state SET state = 'probing', probing_started_at = NOW(), last_attempt_at = NOW() WHERE credential_id = p_credential_id AND raw_model_name = p_raw_model_name AND state = 'suspicious' RETURNING 1) SELECT COUNT(*) > 0 INTO can_probe FROM updated; RETURN can_probe; END; $$;

