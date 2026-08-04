--
-- Name: columnar_drift_report(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.columnar_drift_report() RETURNS TABLE(parent_name text, compliant_count integer, noncompliant_count integer, total_size_bytes bigint, heap_size_bytes bigint, columnar_size_bytes bigint)
    LANGUAGE sql STABLE
    AS $$
    SELECT
        parent_name,
        count(*) FILTER (WHERE compliant) AS compliant_count,
        count(*) FILTER (WHERE NOT compliant) AS noncompliant_count,
        sum(total_size_bytes)::bigint AS total_size_bytes,
        sum(total_size_bytes) FILTER (WHERE storage='heap')::bigint AS heap_size_bytes,
        sum(total_size_bytes) FILTER (WHERE storage='columnar')::bigint AS columnar_size_bytes
    FROM columnar_healthcheck()
    GROUP BY parent_name
    ORDER BY parent_name;
$$;

