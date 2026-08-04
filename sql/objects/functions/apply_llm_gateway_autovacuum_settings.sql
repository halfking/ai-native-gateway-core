--
-- Name: apply_llm_gateway_autovacuum_settings(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.apply_llm_gateway_autovacuum_settings() RETURNS integer
    LANGUAGE plpgsql
    AS $$
		DECLARE
		    opts_sql constant text := '
		        autovacuum_enabled=true,
		        autovacuum_vacuum_scale_factor=0.05,
		        autovacuum_vacuum_threshold=10,
		        autovacuum_analyze_scale_factor=0.02,
		        autovacuum_analyze_threshold=50';
		    r record;
		    applied integer := 0;
		BEGIN
		    FOR r IN
		        SELECT c.relname FROM pg_class c
		        JOIN pg_namespace n ON n.oid = c.relnamespace
		        WHERE n.nspname = 'public' AND c.relkind = 'r'
		          AND (c.relname LIKE '%\_hot' ESCAPE '\'
		            OR c.relname = 'credential_probe_model_log')
		    LOOP
		        BEGIN
		            EXECUTE format('ALTER TABLE %I SET (%s)', r.relname, opts_sql);
		            applied := applied + 1;
		        EXCEPTION WHEN others THEN
		            RAISE NOTICE 'skip autovacuum %: %', r.relname, SQLERRM;
		        END;
		    END LOOP;
		    FOR r IN
		        SELECT c.relname FROM pg_class c
		        JOIN pg_namespace n ON n.oid = c.relnamespace
		        JOIN pg_inherits i ON i.inhrelid = c.oid
		        JOIN pg_class p ON p.oid = i.inhparent
		        WHERE n.nspname = 'public'
		          AND p.relname = ANY (ARRAY[
		              'credential_model_index','model_probe_runs','request_logs',
		              'routing_decision_log','request_wal','usage_ledger',
		              'credit_ledger','tool_usage_stats','candidate_failure_logs',
		              'handoff_logs','request_logs_bodies'])
		    LOOP
		        BEGIN
		            EXECUTE format('ALTER TABLE %I SET (%s)', r.relname, opts_sql);
		            applied := applied + 1;
		        EXCEPTION WHEN others THEN
		            RAISE NOTICE 'skip autovacuum partition %: %', r.relname, SQLERRM;
		        END;
		    END LOOP;
		    RETURN applied;
		END;
		$$;


--
-- Name: FUNCTION apply_llm_gateway_autovacuum_settings(); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.apply_llm_gateway_autovacuum_settings() IS 'Enable aggressive autovacuum/analyze reloptions on llm_gateway hot + partition tables.
Idempotent. Migration 404 / partition_manager startup.';

