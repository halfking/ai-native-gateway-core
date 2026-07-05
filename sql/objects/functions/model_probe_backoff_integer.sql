--
-- Name: model_probe_backoff(integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_backoff(consecutive_failures integer) RETURNS interval
    LANGUAGE sql IMMUTABLE
    AS $$
		    SELECT CASE
			WHEN consecutive_failures <= 0 THEN INTERVAL '30 seconds'
			WHEN consecutive_failures = 1  THEN INTERVAL '2 minutes'
			WHEN consecutive_failures = 2  THEN INTERVAL '5 minutes'
			ELSE                                  INTERVAL '15 minutes'
		    END;
		$$;

