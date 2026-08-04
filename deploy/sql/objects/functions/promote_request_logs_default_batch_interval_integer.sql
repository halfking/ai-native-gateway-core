--
-- Name: promote_request_logs_default_batch(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_request_logs_default_batch(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_rl_batch ON COMMIT DROP AS
    SELECT * FROM public.request_logs_default
    WHERE ts < now() - p_retention
    ORDER BY ts
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.request_logs_default
    WHERE id IN (SELECT id FROM _promote_rl_batch);
    
    BEGIN
        INSERT INTO public.request_logs
        SELECT * FROM _promote_rl_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_request_logs_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

