--
-- Name: trg_cmb_protect_manual_disable(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.trg_cmb_protect_manual_disable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.unavailable_reason = 'manual' THEN
        -- Admin explicit re-enable (toggleModelOfferState available=true)
        IF (NEW.available = TRUE AND NEW.unavailable_reason IS NULL)
           OR current_setting('llmgw.admin_override', true) = '1' THEN
            RETURN NEW;
        END IF;

        IF NEW.unavailable_reason IS DISTINCT FROM 'manual' THEN
            NEW.unavailable_reason := 'manual';
        END IF;
        IF NEW.available = TRUE THEN
            NEW.available := FALSE;
        END IF;
        IF NEW.unavailable_at IS NULL THEN
            NEW.unavailable_at := OLD.unavailable_at;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER cmb_protect_manual_disable BEFORE UPDATE ON public.credential_model_bindings FOR EACH ROW EXECUTE FUNCTION public.trg_cmb_protect_manual_disable();

ALTER TABLE public.credential_model_bindings DISABLE TRIGGER cmb_protect_manual_disable;

