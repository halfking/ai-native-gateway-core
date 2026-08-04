--
-- Name: get_standardized_name(text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.get_standardized_name(p_raw_model_name text) RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_standardized text;
BEGIN
    -- First try model_name_mapping table
    SELECT mnm.standardized_name INTO v_standardized
    FROM public.model_name_mapping mnm
    WHERE lower(mnm.raw_model_name) = lower(p_raw_model_name);
    
    IF v_standardized IS NOT NULL AND v_standardized != '' THEN
        RETURN v_standardized;
    END IF;
    
    -- Then try provider_models.standardized_name
    SELECT pm.standardized_name INTO v_standardized
    FROM public.provider_models pm
    WHERE lower(pm.raw_model_name) = lower(p_raw_model_name)
    LIMIT 1;
    
    IF v_standardized IS NOT NULL AND v_standardized != '' THEN
        RETURN v_standardized;
    END IF;
    
    -- Fallback to raw_model_name
    RETURN p_raw_model_name;
END;
$$;

