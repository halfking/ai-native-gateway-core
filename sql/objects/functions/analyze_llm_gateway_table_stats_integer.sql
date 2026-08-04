--
-- Name: analyze_llm_gateway_table_stats(integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.analyze_llm_gateway_table_stats(p_recent_months integer DEFAULT 2) RETURNS integer
    LANGUAGE plpgsql
    AS $_$
		DECLARE r record; suffix text; m integer; analyzed integer := 0;
		BEGIN
		    IF p_recent_months < 1 THEN p_recent_months := 1;
		    ELSIF p_recent_months > 12 THEN p_recent_months := 12; END IF;
		    FOR r IN
		        SELECT c.relname FROM pg_class c
		        JOIN pg_namespace n ON n.oid = c.relnamespace
		        JOIN pg_am am ON am.oid = c.relam
		        WHERE n.nspname = 'public' AND c.relkind = 'r'
		          AND c.relname LIKE '%\_hot' ESCAPE '\' AND am.amname = 'heap'
		    LOOP
		        EXECUTE format('ANALYZE %I', r.relname); analyzed := analyzed + 1;
		    END LOOP;
		    FOR m IN 0..(p_recent_months - 1) LOOP
		        suffix := to_char(date_trunc('month', now()) - (m || ' months')::interval, 'YYYY_MM');
		        FOR r IN
		            SELECT c.relname FROM pg_class c
		            JOIN pg_namespace n ON n.oid = c.relnamespace
		            WHERE n.nspname = 'public' AND c.relkind = 'r'
		              AND c.relname ~ ('^(credential_model_index|model_probe_runs|request_logs|routing_decision_log|request_wal|usage_ledger|credit_ledger|tool_usage_stats|candidate_failure_logs|handoff_logs|request_logs_bodies)_' || suffix || '$')
		        LOOP
		            EXECUTE format('ANALYZE %I', r.relname); analyzed := analyzed + 1;
		        END LOOP;
		    END LOOP;
		    RETURN analyzed;
		END;
		$_$;

