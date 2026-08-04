--
-- Name: calculate_request_cost(character varying, integer, integer, integer, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.calculate_request_cost(p_model_canonical character varying, p_input_tokens integer, p_output_tokens integer, p_cache_write_tokens integer DEFAULT 0, p_cache_read_tokens integer DEFAULT 0) RETURNS bigint
    LANGUAGE plpgsql STABLE
    AS $$
DECLARE
    v_input_price BIGINT;
    v_output_price BIGINT;
    v_cache_write_price BIGINT;
    v_cache_read_price BIGINT;
    v_total_credits BIGINT;
BEGIN
    SELECT 
        input_credits_per_1m,
        output_credits_per_1m,
        COALESCE(cache_write_credits_per_1m, 0),
        COALESCE(cache_read_credits_per_1m, 0)
    INTO 
        v_input_price,
        v_output_price,
        v_cache_write_price,
        v_cache_read_price
    FROM model_pricing
    WHERE model_canonical = p_model_canonical
        AND active = true;
    
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    
    v_total_credits := 
        (p_input_tokens * v_input_price / 1000000) +
        (p_output_tokens * v_output_price / 1000000) +
        (p_cache_write_tokens * v_cache_write_price / 1000000) +
        (p_cache_read_tokens * v_cache_read_price / 1000000);
    
    RETURN v_total_credits;
END;
$$;

