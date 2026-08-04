--
-- Name: ensure_sessions_v2_partitions(date); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.ensure_sessions_v2_partitions(target_date date DEFAULT CURRENT_DATE) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start DATE := DATE_TRUNC('month', target_date)::DATE;
    month_end DATE := (DATE_TRUNC('month', target_date) + INTERVAL '1 month')::DATE;
    partition_suffix TEXT := TO_CHAR(month_start, 'YYYY_MM');
BEGIN
    -- sessions 分区（heap格式）
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS gateway.sessions_%s PARTITION OF gateway.sessions
         FOR VALUES FROM (%L) TO (%L)',
        partition_suffix, month_start, month_end
    );
    
    -- session_turns 分区（heap格式）
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS gateway.session_turns_%s PARTITION OF gateway.session_turns
         FOR VALUES FROM (%L) TO (%L)',
        partition_suffix, month_start, month_end
    );
    
    -- session_bodies 分区使用 heap，因为正文写入支持冲突更新。
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS gateway.session_bodies_%s PARTITION OF gateway.session_bodies
         FOR VALUES FROM (%L) TO (%L)',
        partition_suffix, month_start, month_end
    );
    
    RAISE NOTICE 'Created sessions V2 partitions for %', partition_suffix;
END;
$$;

