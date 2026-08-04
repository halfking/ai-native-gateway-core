--
-- Name: drop_old_state_partitions(integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.drop_old_state_partitions(p_retention_days integer DEFAULT 30) RETURNS bigint
    LANGUAGE plpgsql
    AS $_$
DECLARE
    total_dropped bigint := 0;
    cutoff_ts timestamptz := NOW() - (p_retention_days || ' days')::interval;
    r record;
    target_tables text[] := ARRAY[
        'routing_decision_log',
        'candidate_failure_logs',
        'handoff_logs',
        'model_probe_runs',
        'credential_model_index'
    ];
    parent_name text;
    partition_name text;
    partition_year int;
    partition_month int;
    partition_first_day date;
BEGIN
    IF p_retention_days < 1 THEN
        RAISE WARNING 'drop_old_state_partitions: retention_days=% < 1, clamping to 1', p_retention_days;
        p_retention_days := 1;
    END IF;

    FOREACH parent_name IN ARRAY target_tables LOOP
        FOR r IN
            SELECT c.relname AS partname
            FROM pg_inherits i
            JOIN pg_class p ON p.oid = i.inhparent
            JOIN pg_class c ON c.oid = i.inhrelid
            WHERE p.relname = parent_name
              AND c.relname ~ ('^' || parent_name || '_\d{4}_\d{2}$')
        LOOP
            partition_name := r.partname;
            -- Parse YYYY_MM from suffix (e.g. "routing_decision_log_2026_07")
            BEGIN
                partition_year := split_part(partition_name, '_', array_length(string_to_array(partition_name, '_'), 1) - 1)::int;
                partition_month := split_part(partition_name, '_', array_length(string_to_array(partition_name, '_'), 1))::int;
                partition_first_day := make_date(partition_year, partition_month, 1);

                -- DROP if entire month is before cutoff
                -- 30-day retention: cutoff = 2026-06-13, drop 2026_05 and earlier
                IF (partition_first_day + INTERVAL '1 month' - INTERVAL '1 day') < cutoff_ts THEN
                    EXECUTE format('DROP TABLE IF EXISTS %I', partition_name);
                    total_dropped := total_dropped + 1;
                    RAISE DEBUG 'drop_old_state_partitions: dropped %', partition_name;
                END IF;
            EXCEPTION WHEN OTHERS THEN
                RAISE WARNING 'drop_old_state_partitions: failed to parse % (%)', partition_name, SQLERRM;
                -- Continue with next partition, don't abort the whole loop
            END;
        END LOOP;
    END LOOP;

    RETURN total_dropped;
END;
$_$;

