--
-- Name: promote_credit_ledger_default_batch(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_credit_ledger_default_batch(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_cl_batch ON COMMIT DROP AS
    SELECT * FROM public.credit_ledger_default
    WHERE created_at < now() - p_retention
    ORDER BY created_at
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.credit_ledger_default
    WHERE id IN (SELECT id FROM _promote_cl_batch);
    
    BEGIN
        INSERT INTO public.credit_ledger
        SELECT * FROM _promote_cl_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_credit_ledger_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

