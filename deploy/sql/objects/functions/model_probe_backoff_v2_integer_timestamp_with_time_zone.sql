--
-- Name: model_probe_backoff_v2(integer, timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_backoff_v2(consecutive_failures integer, last_attempt_at timestamp with time zone) RETURNS interval
    LANGUAGE sql IMMUTABLE
    AS $$
    WITH age AS (
        SELECT EXTRACT(EPOCH FROM (NOW() - COALESCE(last_attempt_at, NOW() - INTERVAL '1 hour'))) AS secs
    )
    SELECT CASE
        -- 0 failures → healthy_confirmed watchdog (every 2h)
        WHEN consecutive_failures <= 0 THEN INTERVAL '2 hours'

        -- 3+ failures → still recovering toward broken_confirmed
        WHEN consecutive_failures >= 3 THEN INTERVAL '60 minutes'

        -- 1 failure: ramp up frequency when fresh, taper when stale
        WHEN consecutive_failures = 1 AND (SELECT secs FROM age) <   300 THEN INTERVAL '1 minute'
        WHEN consecutive_failures = 1 AND (SELECT secs FROM age) <  1800 THEN INTERVAL '3 minutes'
        WHEN consecutive_failures = 1 AND (SELECT secs FROM age) <  3600 THEN INTERVAL '10 minutes'
        WHEN consecutive_failures = 1                              THEN INTERVAL '30 minutes'

        -- 2 failures: same pattern but with longer floor
        WHEN consecutive_failures = 2 AND (SELECT secs FROM age) <   300 THEN INTERVAL '2 minutes'
        WHEN consecutive_failures = 2 AND (SELECT secs FROM age) <  1800 THEN INTERVAL '5 minutes'
        WHEN consecutive_failures = 2 AND (SELECT secs FROM age) <  3600 THEN INTERVAL '15 minutes'
        WHEN consecutive_failures = 2                              THEN INTERVAL '45 minutes'

        -- 4+ failures: very rare, treat like 3+
        ELSE INTERVAL '60 minutes'
    END;
$$;

