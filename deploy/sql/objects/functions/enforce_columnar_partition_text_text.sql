--
-- Name: enforce_columnar_partition(text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enforce_columnar_partition(p_partition_name text, p_parent_name text) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    storage text;
    ok boolean := false;
BEGIN
    SELECT am.amname INTO storage
    FROM pg_class c
    JOIN pg_am am ON am.oid = c.relam
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN pg_inherits i ON i.inhrelid = c.oid
    JOIN pg_class p ON p.oid = i.inhparent
    WHERE n.nspname='public'
      AND c.relname = p_partition_name
      AND p.relname = p_parent_name;

    IF storage IS NULL THEN
        RETURN;
    END IF;
    IF storage = 'columnar' THEN
        RETURN;
    END IF;

    BEGIN
        EXECUTE format('ALTER TABLE public.%I SET ACCESS METHOD columnar',
                       p_partition_name);
        RAISE NOTICE 'enforce_columnar: converted %.% (heap -> columnar)',
                      p_parent_name, p_partition_name;
        ok := true;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'enforce_columnar: %.% conversion failed: %',
                       p_parent_name, p_partition_name, SQLERRM;
    END;
END;
$$;

