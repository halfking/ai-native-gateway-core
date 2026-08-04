--
-- Name: promote_tool_usage_stats_default_batch(interval, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.promote_tool_usage_stats_default_batch(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_tus_batch ON COMMIT DROP AS
    SELECT * FROM public.tool_usage_stats_default
    WHERE usage_date < CURRENT_DATE - (p_retention::int / 86400)  -- 把 interval 转成天数
    ORDER BY usage_date
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.tool_usage_stats_default
    WHERE id IN (SELECT id FROM _promote_tus_batch)
      AND usage_date IN (SELECT usage_date FROM _promote_tus_batch);
    
    BEGIN
        INSERT INTO public.tool_usage_stats
        SELECT * FROM _promote_tus_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_tool_usage_stats_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

