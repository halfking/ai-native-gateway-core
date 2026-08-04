--
-- Name: promote_routing_decision_log_default_batch(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_routing_decision_log_default_batch(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_rdl_batch ON COMMIT DROP AS
    SELECT * FROM public.routing_decision_log_default
    WHERE ts < now() - p_retention
    ORDER BY ts
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.routing_decision_log_default
    WHERE ts IN (SELECT ts FROM _promote_rdl_batch)
      AND request_id IN (SELECT request_id FROM _promote_rdl_batch);
    
    BEGIN
        INSERT INTO public.routing_decision_log
        SELECT * FROM _promote_rdl_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_routing_decision_log_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

