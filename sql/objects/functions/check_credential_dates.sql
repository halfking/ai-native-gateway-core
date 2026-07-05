--
-- Name: check_credential_dates(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.check_credential_dates() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.effective_at IS NOT NULL AND NEW.expires_at IS NOT NULL THEN
        IF NEW.expires_at <= NEW.effective_at THEN
            RAISE EXCEPTION 'expires_at must be greater than effective_at';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

