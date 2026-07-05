--
-- Name: model_offers_delete_trigger(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_offers_delete_trigger() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE credential_model_bindings SET
        available = FALSE,
        unavailable_reason = 'deleted',
        admin_protected = FALSE,
        updated_at = now()
    WHERE id = OLD.id;
    RETURN OLD;
END;
$$;

