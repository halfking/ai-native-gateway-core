--
-- Name: cleanup_stale_in_progress_requests(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.cleanup_stale_in_progress_requests() RETURNS TABLE(cleaned_count bigint)
    LANGUAGE plpgsql
    AS $$
DECLARE
    updated_rows bigint;
BEGIN
    UPDATE request_logs_hot
    SET
        success = false,
        request_status = 'failure',
        error_kind = 'gateway_timeout',
        failure_stage = 'gateway',
        failure_detail_code = 'gw_processing_timeout'
    WHERE
        request_status = 'in_progress'
        AND ts < NOW() - INTERVAL '5 minutes'
        AND success = false;

    GET DIAGNOSTICS updated_rows = ROW_COUNT;

    IF updated_rows > 0 THEN
        RAISE NOTICE 'Cleaned up % stale in_progress request_logs_hot records', updated_rows;
    END IF;

    RETURN QUERY SELECT updated_rows;
END;
$$;

