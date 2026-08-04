--
-- Name: promote_credential_model_index_default_batch(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_credential_model_index_default_batch(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_cmi_batch ON COMMIT DROP AS
    SELECT * FROM public.credential_model_index_default
    WHERE bucket < now() - p_retention
    ORDER BY bucket
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.credential_model_index_default
    WHERE bucket IN (SELECT bucket FROM _promote_cmi_batch)
      AND credential_id IN (SELECT credential_id FROM _promote_cmi_batch)
      AND raw_model IN (SELECT raw_model FROM _promote_cmi_batch);
    
    BEGIN
        INSERT INTO public.credential_model_index
        SELECT * FROM _promote_cmi_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_credential_model_index_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

