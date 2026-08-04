--
-- Name: fn_enforce_columnar_event_trigger(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.fn_enforce_columnar_event_trigger() RETURNS event_trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    parent_name text;
    cmd_record record;
    relkind_table char;
BEGIN
    FOR cmd_record IN
        SELECT *
        FROM pg_event_trigger_ddl_commands()
        WHERE command_tag = 'CREATE TABLE'
    LOOP
        FOREACH parent_name IN ARRAY columnar_insert_only_parents()
        LOOP
            PERFORM enforce_columnar_partition(c.relname, parent_name)
            FROM pg_class c
            JOIN pg_am am ON am.oid = c.relam
            JOIN pg_inherits i ON i.inhrelid = c.oid
            JOIN pg_class p ON p.oid = i.inhparent
            JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE n.nspname = 'public'
              AND p.relname = parent_name
              AND am.amname = 'heap';
        END LOOP;
    END LOOP;
END;
$$;

