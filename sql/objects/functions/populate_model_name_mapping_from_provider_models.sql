--
-- Name: populate_model_name_mapping_from_provider_models(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.populate_model_name_mapping_from_provider_models() RETURNS void
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO public.model_name_mapping (raw_model_name, standardized_name, auto_generated, description)
    SELECT DISTINCT pm.raw_model_name, pm.standardized_name, TRUE, 'Auto-generated from provider_models.standardized_name'
    FROM public.provider_models pm
    WHERE pm.standardized_name IS NOT NULL 
      AND pm.standardized_name != ''
      AND pm.standardized_name != pm.raw_model_name
    ON CONFLICT (raw_model_name) DO UPDATE 
        SET standardized_name = EXCLUDED.standardized_name,
            updated_at = now(),
            auto_generated = TRUE;
END;
$$;

