--
-- Name: ensure_next_month_routing_archive_partition(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.ensure_next_month_routing_archive_partition() RETURNS void
    LANGUAGE plpgsql
    AS $$
		DECLARE
		    next_month_start date := date_trunc('month', now() + interval '1 month')::date;
		    next_month_end   date := date_trunc('month', now() + interval '2 months')::date;
		    partition_name   text := 'routing_decision_log_archive_' || to_char(next_month_start, 'YYYY_MM');
		BEGIN
		    IF NOT EXISTS (SELECT 1 FROM pg_class
		                   WHERE relname = partition_name AND relnamespace = 'public'::regnamespace) THEN
		        EXECUTE format(
		            'CREATE TABLE %I PARTITION OF routing_decision_log_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
		            partition_name, next_month_start, next_month_end
		        );
		    END IF;
		END;
		$$;


--
-- Name: FUNCTION ensure_next_month_routing_archive_partition(); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.ensure_next_month_routing_archive_partition() IS 'Pre-create the next month columnar partition for routing_decision_log_archive. Call this from a cron job or background service at month-end so the columnar partition is ready when archive_routing_decision_log() is called.';

