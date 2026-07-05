--
-- Name: notify_auto_route_refresh(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.notify_auto_route_refresh() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    entity_id text := '';
BEGIN
    IF TG_TABLE_NAME = 'credential_model_bindings' THEN
        entity_id := COALESCE(NEW.credential_id, OLD.credential_id)::text;
    ELSIF TG_TABLE_NAME IN ('credentials', 'api_keys') THEN
        entity_id := COALESCE(NEW.id, OLD.id)::text;
    END IF;

    PERFORM pg_notify('auto_route_refresh',
        TG_TABLE_NAME || ':' || TG_OP || entity_id);
    RETURN COALESCE(NEW, OLD);
END;
$$;

