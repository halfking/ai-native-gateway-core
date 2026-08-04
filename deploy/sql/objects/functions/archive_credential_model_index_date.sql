--
-- Name: archive_credential_model_index(date); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.archive_credential_model_index(archive_month date) RETURNS TABLE(status text, rows_archived bigint, rows_deleted bigint)
    LANGUAGE plpgsql
    AS $$
		DECLARE
		    month_start date := date_trunc('month', archive_month)::date;
		    month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
		    partition_name text := 'credential_model_index_archive_' || to_char(month_start, 'YYYY_MM');
		    archived_count bigint;
		    deleted_count bigint;
		    cutoff_ts timestamptz := NOW() - INTERVAL '7 days';
		BEGIN
		    -- Create target columnar partition if missing
		    IF NOT EXISTS (SELECT 1 FROM pg_class
		                   WHERE relname = partition_name AND relnamespace = 'public'::regnamespace) THEN
		        EXECUTE format(
		            'CREATE TABLE %I PARTITION OF credential_model_index_archive FOR VALUES FROM (%L) TO (%L) USING columnar',
		            partition_name, month_start, month_end
		        );
		    END IF;

		    -- Archive 7d+ data for this month to columnar
		    INSERT INTO credential_model_index_archive
		    SELECT * FROM credential_model_index
		    WHERE bucket >= month_start 
		      AND bucket < month_end
		      AND bucket < cutoff_ts
		    ON CONFLICT DO NOTHING;
		    
		    GET DIAGNOSTICS archived_count = ROW_COUNT;

		    -- Delete archived data from main table
		    DELETE FROM credential_model_index
		    WHERE bucket >= month_start 
		      AND bucket < month_end
		      AND bucket < cutoff_ts;
		    
		    GET DIAGNOSTICS deleted_count = ROW_COUNT;

		    RETURN QUERY SELECT 'success'::text, archived_count, deleted_count;
		END;
		$$;

