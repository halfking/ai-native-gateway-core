--
-- Name: get_setting_int(character varying); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.get_setting_int(setting_key character varying) RETURNS integer
    LANGUAGE plpgsql
    AS $$
DECLARE
    result INTEGER;
BEGIN
    SELECT (value #>> '{}')::INTEGER INTO result
    FROM system_settings
    WHERE key = setting_key;
    
    RETURN result;
END;
$$;

