--
-- Name: columnar_healthcheck(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.columnar_healthcheck() RETURNS TABLE(parent_name text, partition_name text, storage text, expected text, compliant boolean, total_size_bytes bigint, n_live_tup bigint)
    LANGUAGE sql STABLE
    AS $$
    WITH config AS (
        SELECT
            columnar_insert_only_parents() AS should_be_columnar,
            ARRAY['request_logs','request_wal','usage_ledger',
                  'request_logs_archive','request_wal_archive',
                  'usage_ledger_archive']::text[] AS should_be_heap
    ), partitions AS (
        SELECT
            p.relname AS parent_name,
            c.relname AS partition_name,
            CASE WHEN c.relam=(SELECT oid FROM pg_am WHERE amname='columnar') THEN 'columnar'
                 WHEN c.relam=(SELECT oid FROM pg_am WHERE amname='heap') THEN 'heap'
                 ELSE 'other' END AS storage,
            pg_total_relation_size(c.oid) AS total_size_bytes,
            (SELECT n_live_tup FROM pg_stat_user_tables WHERE relid=c.oid) AS n_live_tup
        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = p.relnamespace
        WHERE n.nspname = 'public'
    )
    SELECT
        par.parent_name,
        par.partition_name,
        par.storage,
        CASE
            WHEN par.parent_name = ANY(cfg.should_be_columnar) THEN 'columnar'
            WHEN par.parent_name = ANY(cfg.should_be_heap)     THEN 'heap'
            ELSE 'unknown'
        END::text AS expected,
        (par.storage = CASE
            WHEN par.parent_name = ANY(cfg.should_be_columnar) THEN 'columnar'
            WHEN par.parent_name = ANY(cfg.should_be_heap)     THEN 'heap'
            ELSE NULL END) AS compliant,
        par.total_size_bytes,
        COALESCE(par.n_live_tup, 0)
    FROM partitions par, config cfg
    ORDER BY par.parent_name, par.partition_name;
$$;

