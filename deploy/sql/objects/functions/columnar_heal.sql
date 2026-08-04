--
-- Name: columnar_heal(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.columnar_heal() RETURNS TABLE(parent_name text, partition_name text, converted boolean, pre_size_bytes bigint, post_size_bytes bigint, error_message text)
    LANGUAGE plpgsql
    AS $$
DECLARE
    rec record;
    pre_size bigint;
    post_size bigint;
BEGIN
    FOR rec IN
        SELECT
            p.relname AS parent_name,
            c.relname AS partition_name,
            c.oid AS partition_oid
        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_am am ON am.oid = c.relam
        JOIN pg_namespace n ON n.oid = p.relnamespace
        WHERE n.nspname='public'
          AND am.amname = 'heap'
          AND p.relname = ANY(columnar_insert_only_parents())
    LOOP
        pre_size := pg_total_relation_size(rec.partition_oid);
        BEGIN
            EXECUTE format('ALTER TABLE public.%I SET ACCESS METHOD columnar',
                           rec.partition_name);
            post_size := pg_total_relation_size(rec.partition_oid);
            parent_name := rec.parent_name;
            partition_name := rec.partition_name;
            converted := true;
            pre_size_bytes := pre_size;
            post_size_bytes := post_size;
            error_message := NULL;
            RETURN NEXT;
        EXCEPTION WHEN OTHERS THEN
            parent_name := rec.parent_name;
            partition_name := rec.partition_name;
            converted := false;
            pre_size_bytes := pre_size;
            post_size_bytes := pre_size;
            error_message := SQLERRM;
            RETURN NEXT;
        END;
    END LOOP;
END;
$$;

