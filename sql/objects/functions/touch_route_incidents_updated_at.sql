--
-- Name: touch_route_incidents_updated_at(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.touch_route_incidents_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
		BEGIN
			NEW.updated_at := now();
			RETURN NEW;
		END;
		$$;

