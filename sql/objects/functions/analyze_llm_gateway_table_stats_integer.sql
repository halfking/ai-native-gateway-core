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
		              AND (
                       -- 839: ① 从未被分析过（838 起的首次覆盖保证，往月与当月都适用）
                       NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid)
                    OR (
                       -- ② 当月的非堆（列存）分区仍手工分析：
                       --    生产实测列存分区 autoanalyze_count = 0，autovacuum 不采集，
                       --    交回 autovacuum 就永远没人管（runbook §10.93.6 / §10.99）
                       m = 0
                       AND c.relam <> (SELECT oid FROM pg_am WHERE amname = 'heap')
                      )
                   )
		        LOOP
		            EXECUTE format('ANALYZE %I', r.relname); analyzed := analyzed + 1;
		        END LOOP;
		    END LOOP;
		    RETURN analyzed;
		END;
		$_$;

