--
-- Name: bump_credentials_governor_revision(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.bump_credentials_governor_revision() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.revision := nextval('public.credentials_governor_revision_seq');
    ELSIF OLD.concurrency_limit IS DISTINCT FROM NEW.concurrency_limit
       OR OLD.concurrency_mode IS DISTINCT FROM NEW.concurrency_mode
       OR OLD.rpm_limit IS DISTINCT FROM NEW.rpm_limit
       OR OLD.tpm_limit IS DISTINCT FROM NEW.tpm_limit
       OR OLD.fp_slot_limit IS DISTINCT FROM NEW.fp_slot_limit
       OR OLD.max_queue_depth IS DISTINCT FROM NEW.max_queue_depth
       OR OLD.max_queue_wait_ms IS DISTINCT FROM NEW.max_queue_wait_ms THEN
        NEW.revision := nextval('public.credentials_governor_revision_seq');
    ELSE
        NEW.revision := OLD.revision;
    END IF;
    RETURN NEW;
END;
$$;
