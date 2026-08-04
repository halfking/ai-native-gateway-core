--
-- Name: auto_set_fp_slot_limit(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.auto_set_fp_slot_limit() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    -- Auto-fill fp_slot_limit from concurrency_limit if not explicitly set
    IF NEW.fp_slot_limit IS NULL THEN
        IF NEW.concurrency_limit IS NOT NULL AND NEW.concurrency_limit > 0 THEN
            NEW.fp_slot_limit := GREATEST(1, NEW.concurrency_limit / 4);
        ELSE
            NEW.fp_slot_limit := 20;  -- 2026-06-24: 5→20, matches DefaultDefaultLimit
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

