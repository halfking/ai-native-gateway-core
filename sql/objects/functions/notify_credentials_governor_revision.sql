--
-- Name: notify_credentials_governor_revision(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.notify_credentials_governor_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_notify('credentials_revision', NEW.revision::text);
    RETURN NEW;
END;
$$;
