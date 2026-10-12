-- 856_analyze_columnar_staleness_gate.down.sql
-- 回滚：移除陈旧度闸门，恢复「当月列存分区每轮都手工 analyze」的旧行为。
-- ⚠ 回滚会让数据库时间回到约 171 分钟/天，并让 MV 刷新重新被这趟 pass 的
--   长快照拖住（runbook §10.110.14）。仅在确认新闸门造成计划劣化时才回滚。

CREATE OR REPLACE FUNCTION public.analyze_llm_gateway_table_stats(p_recent_months integer DEFAULT 2)
    RETURNS integer
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
