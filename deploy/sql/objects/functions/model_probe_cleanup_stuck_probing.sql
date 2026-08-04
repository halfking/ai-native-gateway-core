--
-- Name: model_probe_cleanup_stuck_probing(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_cleanup_stuck_probing() RETURNS integer
    LANGUAGE plpgsql
    AS $$ DECLARE cleaned_count INTEGER; BEGIN WITH cleaned AS (UPDATE model_probe_state SET state = 'suspicious', probing_started_at = NULL, next_retry_at = NOW() + INTERVAL '2 minutes' WHERE state = 'probing' AND probing_started_at IS NOT NULL AND probing_started_at < NOW() - INTERVAL '5 minutes' RETURNING 1) SELECT COUNT(*) INTO cleaned_count FROM cleaned; RETURN cleaned_count; END; $$;

