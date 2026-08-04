--
-- Name: log_model_pricing_change(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.log_model_pricing_change() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.input_credits_per_1m != NEW.input_credits_per_1m 
        OR OLD.output_credits_per_1m != NEW.output_credits_per_1m THEN
        
        INSERT INTO model_pricing_history (
            model_canonical,
            old_input_credits_per_1m,
            old_output_credits_per_1m,
            new_input_credits_per_1m,
            new_output_credits_per_1m,
            change_reason
        ) VALUES (
            NEW.model_canonical,
            OLD.input_credits_per_1m,
            OLD.output_credits_per_1m,
            NEW.input_credits_per_1m,
            NEW.output_credits_per_1m,
            'Price update via migration or admin API'
        );
    END IF;
    
    RETURN NEW;
END;
$$;

