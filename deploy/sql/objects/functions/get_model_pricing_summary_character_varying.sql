--
-- Name: get_model_pricing_summary(character varying); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.get_model_pricing_summary(p_model_canonical character varying) RETURNS TABLE(model character varying, display_name character varying, input_price_cny numeric, output_price_cny numeric, tier character varying, active boolean)
    LANGUAGE plpgsql STABLE
    AS $$
BEGIN
    RETURN QUERY
    SELECT 
        mp.model_canonical,
        mp.display_name,
        ROUND(mp.input_credits_per_1m * ms.cents_per_credit / 100.0, 2) as input_price_cny,
        ROUND(mp.output_credits_per_1m * ms.cents_per_credit / 100.0, 2) as output_price_cny,
        mp.tier,
        mp.active
    FROM model_pricing mp
    CROSS JOIN maas_settings ms
    WHERE mp.model_canonical = p_model_canonical;
END;
$$;

