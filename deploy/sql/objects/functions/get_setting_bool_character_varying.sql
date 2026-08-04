--
-- Name: get_setting_bool(character varying); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.get_setting_bool(setting_key character varying) RETURNS boolean
    LANGUAGE plpgsql
    AS $$
DECLARE
    result BOOLEAN;
BEGIN
    SELECT (value #>> '{}')::BOOLEAN INTO result
    FROM system_settings
    WHERE key = setting_key;
    
    RETURN result;
END;
$$;

