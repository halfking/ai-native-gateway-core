--
-- Name: cleanup_old_credential_model_index(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.cleanup_old_credential_model_index() RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    deleted_count bigint := 0;
    total_count bigint := 0;
    cutoff_ts timestamptz := NOW() - INTERVAL '7 days';
BEGIN
    -- Hot table is heap, supports DELETE directly.
    DELETE FROM credential_model_index_hot
    WHERE bucket < cutoff_ts;
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    total_count := total_count + deleted_count;

    -- Columnar (monthly) partitions don't support DELETE/CTID scans.
    -- The archive function handles them; if archive hasn't run for 7d
    -- the rows stay, which is fine — they get reaped by archive_old_credential_model_index()
    -- (day 3 monthly, twoMonthsAgo). So for now we skip the columnar
    -- partitions here. Hot is the recent cache, archive is the historical
    -- store. This is consistent with how request_logs bodies works.

    RETURN total_count;
END;
$$;


--
-- Name: FUNCTION cleanup_old_credential_model_index(); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.cleanup_old_credential_model_index() IS 'Daily cleanup: removes credential_model_index rows older than 7 days from main table. Assumes historical data has been archived to credential_model_index_archive. Run daily at 3AM via background worker or cron.';

