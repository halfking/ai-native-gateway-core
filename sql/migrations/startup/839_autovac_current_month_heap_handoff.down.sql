-- 839_autovac_current_month_heap_handoff.down.sql
-- 回滚：恢复 838 的函数体，并把当月堆分区的 autovacuum_analyze_scale_factor
-- 交还给 404 统一的 0.02。
--
-- 回滚后手工 pass 会重新每小时分析当月堆分区（§10.99.5 的方案随之作废），
-- autovacuum 再次被清零计数器挡在门外。
--
-- ⚠ 2026-10-07 审计注：db/db.go ensurePartitionAutovacuumSchema 的内联副本
-- （每次启动 CREATE OR REPLACE 的第五份活拷贝）自同日起与 839 正本同步——
-- 回滚 839 必须连同回退该副本（或后续迁移），否则下次重启会把旧函数体与
-- 0.005 重新装回去，回滚不生效。

BEGIN;

-- 838 的函数体：当月无条件分析，往月仅补从未分析过。
CREATE OR REPLACE FUNCTION public.analyze_llm_gateway_table_stats(p_recent_months integer DEFAULT 2) RETURNS integer
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
		              AND (m = 0 OR NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid))
		        LOOP
		            EXECUTE format('ANALYZE %I', r.relname); analyzed := analyzed + 1;
		        END LOOP;
		    END LOOP;
		    RETURN analyzed;
		END;
		$_$;

-- 交还 scale_factor：复用 839 建的那个函数，传 0.02。
-- 注意必须用 to_regprocedure（函数名对 to_regclass 恒返回 NULL——
-- 首版误用导致回滚静默少做一半，2026-10-07 审计订正）。
DO $$
BEGIN
    IF to_regprocedure('public.apply_llm_gateway_current_month_analyze_scale_factor(numeric)') IS NOT NULL THEN
        PERFORM apply_llm_gateway_current_month_analyze_scale_factor(0.02);
    END IF;
END $$;

DROP FUNCTION IF EXISTS public.apply_llm_gateway_current_month_analyze_scale_factor(numeric);

COMMIT;