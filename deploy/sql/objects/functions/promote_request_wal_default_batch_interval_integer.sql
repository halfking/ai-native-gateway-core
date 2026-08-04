--
-- Name: promote_request_wal_default_batch(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_request_wal_default_batch(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_wal_batch ON COMMIT DROP AS
    SELECT * FROM public.request_wal_default
    WHERE created_at < now() - p_retention
    ORDER BY created_at
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.request_wal_default
    WHERE request_id IN (SELECT request_id FROM _promote_wal_batch)
      AND created_at IN (SELECT created_at FROM _promote_wal_batch);
    
    BEGIN
        INSERT INTO public.request_wal
        SELECT * FROM _promote_wal_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_request_wal_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

