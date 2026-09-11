--
-- Name: ensure_candidate_failure_logs_partition(timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE OR REPLACE FUNCTION public.ensure_candidate_failure_logs_partition(target_ts timestamp with time zone) RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date;
    month_end      date;
    partition_name text;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name := 'candidate_failure_logs_' || to_char(month_start, 'YYYY_MM');

    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- 689: heap (was columnar). Row-level DELETE (the 7d TTL trim path
        -- in bg/opslog_trimmer.go) and the hot→monthly promote chain both
        -- need UPDATE/DELETE-capable storage; columnar partitions are
        -- append-only (established by migration 562).
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF candidate_failure_logs
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_candidate_failure_logs_partition: created % as heap', partition_name;
    END IF;
    -- 689: dropped the former ELSE-branch enforce_columnar_partition() call —
    -- partitions are heap now and must stay heap.
    RETURN partition_name;
END;
$$;

--
-- Name: FUNCTION ensure_candidate_failure_logs_partition(target_ts timestamp with time zone); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION ensure_candidate_failure_logs_partition(timestamp with time zone) IS
'Ensure monthly partition for candidate_failure_logs (heap since 689).
Changed from columnar to heap by Migration 689 (2026-09-09): columnar
partitions are append-only, which broke the 7d TTL row-level DELETE path
(bg/opslog_trimmer.go) and the hot→monthly promote chain.';

