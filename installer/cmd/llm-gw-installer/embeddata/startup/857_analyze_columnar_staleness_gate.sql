-- 857_analyze_columnar_staleness_gate.sql
-- 2026-10-13: 给 analyze_llm_gateway_table_stats 的「当月列存分区」分支加**陈旧度闸门**。
--
-- 背景（runbook §10.110.14，2026-10-13 生产实测）：
--   函数体两段。第二段按 12 个表族选月分区，谓词是
--     ① 从未被分析过（pg_statistic 无行）           —— 838 起
--     ② m=0（当月）且非堆（列存）分区              —— 839 起
--   ② **不看统计是否已经新鲜**，于是三张当月列存分区**每一轮都被无差别重扫**：
--     request_logs_bodies_2026_10   columnar 529 MB / 666,124 行
--     routing_decision_log_2026_10  columnar  30 MB /  83,355 行
--     credential_model_index_2026_10 columnar 19 MB / 97,427 行
--   实测三张的 `n_mod_since_analyze` **全是 0**（距上次 analyze 4 小时内零改动），
--   而 `SELECT analyze_llm_gateway_table_stats($1)` 的实测是
--     40 次调用 / mean 256.82 s / max 582.30 s / 累计 171.2 分钟（stats_reset 2026-10-10 16:11）
--   ⇒ **约 171 分钟/天的数据库时间，花在重复扫描统计早已新鲜的列存分区上。**
--
--   放大效应：REFRESH MATERIALIZED VIEW CONCURRENTLY 的第二阶段必须等所有更老的快照结束
--   ⇒ 这趟 pass 持有的 256~582 秒事务，会让**每一个** MV 刷新都跟着等
--   （这正是 §10.110.13 里「视图查询热态 1.2 s、刷新却报 144 s」的一半原因）。
--
-- 改法：② 增加 `距上次 analyze 超过 6 小时` 才重跑。
--   - 每日 4 次 vs 原每小时 ⇒ 单趟这部分降约 75%（实际调用频率受 840 的节流槽约束更低）。
--   - ①（从未分析过）不受影响，仍保证新分区第一次就被 analyze。
--   - 语义不变：只是把「每轮都做」改成「陈旧才做」，不改选表集合的定义。
--
-- 为什么不用 `n_mod_since_analyze` 作判据：列存表的该计数是否可靠更新
--   没有独立证据（实测恒为 0，无法区分「真没变」与「没更新」），
--   而 `pg_stat_all_tables.last_analyze` 是 analyze 自己写的、确定可靠。
--
-- ⚠ 本函数体有**五份活副本**，缺一份就会在重启后被旧体覆盖（§10.109 的教训）：
--   sql/objects/functions/analyze_llm_gateway_table_stats_integer.sql   规范源
--   db/db.go                                                         每次启动 CREATE OR REPLACE
--   sql/schema/01-schema.sql · deploy/sql/schemas/baseline/01-schema.sql
--   installer/cmd/llm-gw-installer/embeddata/01-schema.sql
--   —— 已同步，改动必须四处一起改（migration_857_test.go 把这件事钉成门）。

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
                         -- 857（2026-10-13）：「需要手工」不等于「每轮都做」。
                         --    实测三张当月列存分区 n_mod_since_analyze 全为 0，
                         --    却被每小时无差别重扫（request_logs_bodies_2026_10 单表
                         --    529 MB / 666,124 行），整趟 pass 已占数据库时间约 171 分钟/天；
                         --    且它持有的快照会让每次 REFRESH MATERIALIZED VIEW CONCURRENTLY
                         --    等它（runbook §10.110.14）。⇒ 改为「统计陈旧时才手工」：
                         --    距上次 analyze 超过 6 小时才重跑（每日 4 次，较原每小时降 75%）。
                         --    「从未分析过」由上面的 ① 单独兜住，不受本门影响。
                         AND COALESCE(
                               (SELECT t.last_analyze FROM pg_stat_all_tables t WHERE t.relid = c.oid),
                               '-infinity'::timestamptz
                             ) < now() - interval '6 hours'
                      )
                   )
		        LOOP
		            EXECUTE format('ANALYZE %I', r.relname); analyzed := analyzed + 1;
		        END LOOP;
		    END LOOP;
		    RETURN analyzed;
		END;
		$_$;
