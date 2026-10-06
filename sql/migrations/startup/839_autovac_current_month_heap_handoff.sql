-- 839_autovac_current_month_heap_handoff.sql
-- 2026-10-07: 当月堆分区交回 autovacuum，并下调其 autovacuum_analyze_scale_factor。
--
-- 背景（runbook §10.92~§10.93.5、§10.99）：
--   ① analyze_llm_gateway_table_stats 每小时跑一趟，是网关库数据库时间第一名
--      （81.0%，约 56 分钟/天）。当月**堆**分区占整趟 40.2%（§10.93.5）。
--   ② 手工 pass 每小时 ANALYZE ⇒ 清零 n_mod_since_analyze ⇒ autovacuum 永不就手。
--      生产侧实测 22/22 个关系的 n_mod_since_analyze 全为 0（§10.93.6）。
--   ③ 交回 autovacuum 后，触发线 = 50 + 0.02 × 行数 = 2,402 行，
--      实测写入 834 行/小时 ⇒ 最坏间隔 6h/8h/11h（当前/月中/月末）。
--      原 §10.93.5 估的「55 小时以上」高估约 18 倍。
--
-- 本迁移做两件事，必须成对：
--   A. analyze_llm_gateway_table_stats：当月**堆**分区不再无条件分析；
--      「列存分区」与「从未分析过（pg_statistic 无行）」两类仍必须分析。
--      ★ 列存不能交回：生产实测列存分区 autoanalyze_count = 0（§10.93.6），
--        autovacuum 对 columnar 不做统计采集，交回就永远没人管。
--      ★ 首次覆盖不能交回：缺 pg_statistic 行的分区必须补，否则从未被分析过。
--   B. 当月堆分区额外下调 autovacuum_analyze_scale_factor 到 0.005，
--      把最坏间隔从 6h/11h 约束到 3h/4h。
--      ★ autoanalyze 单次开销与 scale_factor 无关（按 300×default_statistics_target
--        采样，上限 30 万行），增加的只是触发次数；按 0.005 算每天约 8 次，
--        比这些分区现在被每小时手工分析的 96 次/天**更少**。
--
-- 回滚判据（部署后）：24 小时内当月堆分区的 autoanalyze_count 未上升
-- ⇒ 假设被证伪，立即回滚（.down.sql 恢复 0.02 与旧函数体）。

BEGIN;

-- ── A. analyze 函数：当月堆分区交回 autovacuum ──────────────────────────
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
		              AND (
		                    -- ① 从未被分析过（838 起的首次覆盖保证，往月与当月都适用）
		                    NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid)
		                 OR (
		                        -- ② 当月的非堆（列存）分区仍手工分析：
		                        --    生产实测列存 autoanalyze_count = 0，autovacuum 不采集
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

-- ── B. 当月堆分区下调 autovacuum_analyze_scale_factor ────────────────────
-- 只针对「当前月、堆、且是某张时间分区父表的子分区」，
-- 其余关系维持 404 设的 0.02。
CREATE OR REPLACE FUNCTION apply_llm_gateway_current_month_analyze_scale_factor(p_scale_factor numeric DEFAULT 0.005)
RETURNS integer
LANGUAGE plpgsql
AS $function$
DECLARE
    cur_suffix constant text := to_char(date_trunc('month', now()), 'YYYY_MM');
    heap_oid   constant oid   := (SELECT oid FROM pg_am WHERE amname = 'heap');
    r record;
    applied integer := 0;
BEGIN
    FOR r IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_inherits i ON i.inhrelid = c.oid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE n.nspname = 'public'
          AND c.relkind = 'r'
          AND c.relam = heap_oid
          AND p.relname = ANY (ARRAY[
              'credential_model_index', 'model_probe_runs', 'request_logs',
              'routing_decision_log', 'request_wal', 'usage_ledger',
              'credit_ledger', 'tool_usage_stats', 'candidate_failure_logs',
              'handoff_logs', 'request_logs_bodies'
          ])
          AND c.relname ~ ('_' || cur_suffix || '$')
    LOOP
        BEGIN
            EXECUTE format(
                'ALTER TABLE %I SET (autovacuum_analyze_scale_factor = %s)',
                r.relname, p_scale_factor);
            applied := applied + 1;
        EXCEPTION
            WHEN undefined_table THEN NULL;
            WHEN others THEN
                RAISE NOTICE 'apply_llm_gateway_current_month_analyze_scale_factor: skip % (%).',
                    r.relname, SQLERRM;
        END;
    END LOOP;
    RETURN applied;
END;
$function$;

SELECT apply_llm_gateway_current_month_analyze_scale_factor(0.005);

COMMIT;