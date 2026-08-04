--
-- Name: create_next_month_partitions(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.create_next_month_partitions() RETURNS text
    LANGUAGE plpgsql
    AS $$
DECLARE
    next_month_start date;
    next_month_end date;
    month_suffix text;
    result text := '';
BEGIN
    next_month_start := date_trunc('month', now() + interval '1 month');
    next_month_end := date_trunc('month', now() + interval '2 months');
    month_suffix := to_char(next_month_start, 'YYYY_MM');
    
    -- usage_ledger
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS usage_ledger_%s
        PARTITION OF usage_ledger
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    result := result || 'usage_ledger_' || month_suffix || ', ';
    
    -- credit_ledger
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS credit_ledger_%s
        PARTITION OF credit_ledger
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    result := result || 'credit_ledger_' || month_suffix || ', ';
    
    -- tool_usage_stats
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS tool_usage_stats_%s
        PARTITION OF tool_usage_stats
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    result := result || 'tool_usage_stats_' || month_suffix || ', ';
    
    -- request_logs
    EXECUTE format('
        CREATE TABLE IF NOT EXISTS request_logs_%s
        PARTITION OF request_logs
        FOR VALUES FROM (%L) TO (%L)
        USING heap',
        month_suffix, next_month_start, next_month_end);
    result := result || 'request_logs_' || month_suffix;
    
    RETURN '✅ Created partitions for ' || month_suffix || ': ' || result;
END;
$$;


--
-- Name: FUNCTION create_next_month_partitions(); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.create_next_month_partitions() IS '自动创建下个月的所有 telemetry 表分区（heap 存储）。每月28日执行。';

