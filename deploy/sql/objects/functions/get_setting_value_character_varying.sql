--
-- Name: get_setting_value(character varying); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.get_setting_value(setting_key character varying) RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE
    result TEXT;
BEGIN
    SELECT value #>> '{}' INTO result
    FROM system_settings
    WHERE key = setting_key;
    
    RETURN result;
END;
$$;

