--
-- Name: cleanup_old_credential_probe_model_log(integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.cleanup_old_credential_probe_model_log(p_retention_days integer DEFAULT 90) RETURNS bigint
    LANGUAGE plpgsql
    AS $$
DECLARE
    deleted_count bigint;
    cutoff_ts timestamptz := NOW() - (p_retention_days || ' days')::interval;
BEGIN
    IF p_retention_days < 7 THEN
        RAISE WARNING 'cleanup_old_credential_probe_model_log: retention_days=% < 7, clamping to 7', p_retention_days;
        p_retention_days := 7;
    END IF;

    DELETE FROM credential_probe_model_log
    WHERE created_at < cutoff_ts;

    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN deleted_count;
END;
$$;

