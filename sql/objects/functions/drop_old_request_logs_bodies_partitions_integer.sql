--
-- Name: drop_old_request_logs_bodies_partitions(integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.drop_old_request_logs_bodies_partitions(p_retention_days integer) RETURNS TABLE(dropped_partition text, rows_dropped bigint)
    LANGUAGE plpgsql
    AS $_$
DECLARE
    cutoff_date date := CURRENT_DATE - p_retention_days;
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT
            c.relname AS partition_name,
            pg_total_relation_size(c.oid) AS size_bytes
        FROM pg_class c
        JOIN pg_inherits i ON i.inhrelid = c.oid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = 'request_logs_bodies'
          AND c.relname ~ '^request_logs_bodies_[0-9]{4}_[0-9]{2}$'
    LOOP
        DECLARE
            partition_month date := to_date(
                substring(rec.partition_name FROM 'request_logs_bodies_([0-9]{4}_[0-9]{2})$'),
                'YYYY_MM'
            );
            month_end date := partition_month + interval '1 month';
        BEGIN
            IF month_end <= cutoff_date THEN
                EXECUTE format('DROP TABLE %I', rec.partition_name);
                RAISE NOTICE 'drop_old_request_logs_bodies_partitions: dropped %', rec.partition_name;
                dropped_partition := rec.partition_name;
                rows_dropped := -1;
                RETURN NEXT;
            END IF;
        END;
    END LOOP;
END;
$_$;

