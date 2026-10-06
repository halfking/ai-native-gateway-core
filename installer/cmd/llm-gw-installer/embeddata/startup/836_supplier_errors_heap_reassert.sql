-- ===========================================================================
-- File:          sql/migrations/startup/836_supplier_errors_heap_reassert.sql
-- Migration:     836
-- Database:      llm_gateway
-- Purpose:        重新断言 supplier_errors 族的**最终形态**，并把现存的列存
--                分区转回 heap —— 813 明明在台账里、却从未在活库生效。
--
-- Status:        active
-- Idempotent:    YES（新文件无台账行 ⇒ 每次部署都跑；本迁移自身幂等：
--                 函数 CREATE OR REPLACE，转换块循环空集）
-- Dependencies:  813（ensure 函数体与分区转换块的原始出处）、01-schema 基线
--                 （columnar_healthcheck 的出处）
--
-- ── 立项依据（2026-10-06 真机读数 + 台账读数，不是推演）────────────────
--
-- 活库（127.0.0.1:5432/llm_gateway）当前状态：
--
--   1) ensure_supplier_errors_partition 的活体含 USING columnar，且 ELSE 分支
--      调 enforce_columnar_partition —— 即 V371 的列存版，不是 813 的 heap 版。
--   2) 三个分区 supplier_errors_2026_09 / _10 / _11 的 relam **全是 columnar**。
--   3) columnar_healthcheck() 的活体**不含** supplier_errors，于是它对这三个
--      分区一律报 expected='unknown' ⇒ **一条告警都不会响**。仓库基线里的同名
--      函数是含的（should_be_heap 数组末项即 'supplier_errors'）。
--
-- 而台账说 813 已经应用过了：
--
--   gateway_db_revision_sequences
--     ...:V371__supplier_errors_hot_and_stats.sql  2026-09-05 17:48  sha=(null)
--     ...:813_supplier_errors_partitions_heap.sql  2026-10-02 13:51  sha=72c39970...
--
-- ★ **这就是根因，也是它此前一直没被找到的原因**：
--   apply-db-revision-sequence.sh 的跳过条件是
--   stored_sha == file_sha => 打印 "already applied" 并跳过。
--   813 的文件 sha256 与台账存的**完全一致**（都是 72c39970...）⇒ 每次部署都
--   跳过它，**永远不会有任何东西来修**。V371 同理（sha 为 NULL ⇒ 走「台账有行
--   且不在 legacy_content_replays ⇒ 跳过」分支）。
--
--   而 intentional_function_chains 里
--     ensure_supplier_errors_partition|V371|699|813|
--   早已登记，注释还写着「813 必须是最后一项」。那个守卫只校验**登记**，不校验
--   **执行** ⇒ 「最后一个赢」在任一成员被幂等跳过时就不成立。
--   本仓对同一形态已有先例：572 被 563 覆盖，于是 661 来 re-assert。
--   本迁移就是 813 的那次 re-assert。
--
-- ── 为什么不直接删台账行 ──────────────────────────────────────────────
--
-- 删 gateway_db_revision_sequences 里 813 那一行确实能让它重跑，但那是**一次性
-- 的人工动作**：换一台库、或有人重建台账，就又回到「没人修」的状态。
-- ⇒ 修法必须落在受追踪链里，这样任何环境跑部署都会自愈。
--
-- ── 风险：这一族分区里**有数据** ──────────────────────────────────────
--
-- 2026-10-06 真机 count(*) 读数。刻意不用 reltuples / n_live_tup —— 那两个是
-- 未采集的统计（实测 reltuples=-1、n_live_tup=0），而真值是 3,222 行。
-- 静态读数会把「有数据」说成「空壳」，于是转换通道会选错那一条。
--
--     supplier_errors_2026_09    0 行
--     supplier_errors_2026_10    3,222 行   ← 走带数据通道
--     supplier_errors_2026_11    0 行
--
-- ⇒ 必须走 813 的**带数据**通道（DETACH → 改名 bak → 同边界重建 heap 叶 →
--   回拷 → 行数守恒校验 → DROP bak），不能只做空壳重建。
--
-- ── 载荷三段全部逐字摘自出处 ──────────────────────────────────────────
--
--   1) ensure_supplier_errors_partition —— 摘自 813（813 自述与三基线一致；
--      生成脚本断言了两者**可执行体**相等，不等就中止）
--   2) columnar_healthcheck            —— 摘自 sql/schema/01-schema.sql
--      **一处刻意偏离**：基线写的是 CREATE FUNCTION（全新安装时该函数还不存在），
--      而 836 跑在**已经有**这个函数的库上，裸 CREATE FUNCTION 会以
--      "function already exists" 中止 —— 也就是迁移会在它专门要修的那些库上失败。
--      故语句改写为 CREATE OR REPLACE FUNCTION，**函数体逐字不变**。
--   3) 分区转换 DO 块                  —— 逐字摘自 813 的第 2 步
--
-- ★ 「逐字一致」在 1) 上有一处例外：813 与基线的 ensure 函数体**注释措辞不同**
--   （可执行体相同）。813 自己的纪律是「历史迁移文件本体不动」，所以不去改
--   813，改由 bg/supplier_errors_heap_reassert_test.go 比剥掉注释后的可执行体。
--
-- 2)3) 由 bg/supplier_errors_heap_reassert_test.go 钉住逐字一致。之所以要有这条
--   门：三段都是复制品，而复制品之间**会各自漂移** —— 813 的文件头就是为此写的
--   「三基线一致性契约测试钉同形」。
--
-- ★ 那条门还钉了一件本文件自己踩过的事：**整段 CREATE 语句必须在**。
--   本文件的第一版抽取只取了 $$…$$ 函数体，把 CREATE 签名漏掉了，于是交付的是
--   一个「裸 $$ 加函数体」的语法死文件 —— 而当时的逐字自证**是绿的**，因为它在
--   两边抽的是同一个被截断的片段。⇒ 派生量与真值同形时要去读定义：判据必须
--   断言「语句条数」，而不是断言「我抽的那段两边一样」。
-- ===========================================================================

BEGIN;

SET LOCAL statement_timeout = '10min';
-- 与 813 同款：pg_get_expr 渲染的分区边界随会话时区变化，钉扎 +08 使重建边界
-- 与日志/台账指纹在同一渲染口径下可审计。
SET LOCAL TIME ZONE 'Asia/Shanghai';

-- ===========================================================================
-- 1. ensure_supplier_errors_partition -> heap 版（逐字摘自 813）
--    先换函数：否则每小时 ensure tick 的 ELSE 分支会把刚转好的分区再转回
--    columnar。顺序不能与第 3 步对调。
-- ===========================================================================

CREATE OR REPLACE FUNCTION public.ensure_supplier_errors_partition(target_ts timestamp with time zone)
RETURNS text
LANGUAGE plpgsql
AS $$
DECLARE
    month_start    date;
    month_end      date;
    partition_name text;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name := 'supplier_errors_' || to_char(month_start, 'YYYY_MM');

    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- 813: heap（was columnar）。supplier_errors 是 R18 正典单族
        -- {routing_decision_log} 之外的漂移残留；列存对本族只有风险
        -- （UPDATE/DELETE/tableoid 读毒面）而无收益（TTL 是整分区 DROP、
        -- 读写皆低频）。enforce_columnar_partition 的 ELSE 分支同步移除，
        -- 防每小时 ensure tick 把分区转回 columnar。
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF supplier_errors
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_supplier_errors_partition: created % as heap', partition_name;
    END IF;
    RETURN partition_name;
END;
$$;

-- ===========================================================================
-- 2. columnar_healthcheck -> 含 supplier_errors 的现行版（逐字摘自基线）
--
--    这一段与第 1、3 步同等重要：没有它，转换完成后健康检查对这一族仍然报
--    expected='unknown'，也就是「漂移存在但看不见」这个状态本身。
--    真正能防复发的是让健康面开始对它说话。
-- ===========================================================================

CREATE OR REPLACE FUNCTION public.columnar_healthcheck() RETURNS TABLE(parent_name text, partition_name text, storage text, expected text, compliant boolean, total_size_bytes bigint, n_live_tup bigint)
    LANGUAGE sql STABLE
    AS $$
    WITH config AS (
        SELECT
            columnar_insert_only_parents() AS should_be_columnar,
            ARRAY['request_logs','request_wal','usage_ledger',
                  'request_logs_archive','request_wal_archive',
                  'usage_ledger_archive',
                  -- R17 rolled-back families (20): before 2026-10-01 these fell
                  -- through to expected='unknown', so the detector for the
                  -- outage that actually happened was blind to them.
                  'sessions','session_turns','session_turn_details',
                  'session_bodies','usage_facts','stats_event_inbox',
                  'credential_model_index','auto_route_selections',
                  'session_memora','session_censors','system_probe_runs',
                  'credit_ledger','tool_usage_stats','session_tools',
                  'session_module_executions','cache_metrics',
                  'dashboard_access_events','model_probe_runs','handoff_logs',
                  'supplier_errors']::text[] AS should_be_heap
    ), partitions AS (
        SELECT
            p.relname AS parent_name,
            c.relname AS partition_name,
            CASE WHEN c.relam=(SELECT oid FROM pg_am WHERE amname='columnar') THEN 'columnar'
                 WHEN c.relam=(SELECT oid FROM pg_am WHERE amname='heap') THEN 'heap'
                 ELSE 'other' END AS storage,
            pg_total_relation_size(c.oid) AS total_size_bytes,
            (SELECT n_live_tup FROM pg_stat_user_tables WHERE relid=c.oid) AS n_live_tup
        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = p.relnamespace
        WHERE n.nspname = 'public'
          AND p.relkind = 'p'
          AND c.relkind = 'r'
    )
    SELECT
        par.parent_name,
        par.partition_name,
        par.storage,
        CASE
            WHEN par.parent_name = ANY(cfg.should_be_columnar) THEN 'columnar'
            WHEN par.parent_name = ANY(cfg.should_be_heap)     THEN 'heap'
            -- 'unknown' is a legitimate third state, not just a gap: some
            -- families are columnarised per-partition by policy
            -- (request_logs_bodies, routing_decision_log_archive) and must
            -- stay out of BOTH lists -- widening should_be_columnar means
            -- editing the pinned columnar_insert_only_parents() SSOT.
            ELSE 'unknown'
        END::text AS expected,
        (par.storage = CASE
            WHEN par.parent_name = ANY(cfg.should_be_columnar) THEN 'columnar'
            WHEN par.parent_name = ANY(cfg.should_be_heap)     THEN 'heap'
            ELSE NULL END) AS compliant,
        par.total_size_bytes,
        COALESCE(par.n_live_tup, 0)
    FROM partitions par, config cfg
    ORDER BY par.parent_name, par.partition_name;
$$;

-- ===========================================================================
-- 3. 现存列存分区转 heap（逐字摘自 813 第 2 步，含带数据通道）
-- ===========================================================================

DO $$
DECLARE
    part        record;
    v_rows      bigint;
    v_bak       text;
    v_bak_rows  bigint;
    v_new_rows  bigint;
BEGIN
    -- to_regclass 守卫（612 层纪律）：目标表缺席的库上直通跳过。
    IF to_regclass('public.supplier_errors') IS NULL THEN
        RAISE NOTICE '813: public.supplier_errors absent; nothing to convert';
        RETURN;
    END IF;

    FOR part IN
        SELECT c.relname AS name,
               pg_get_expr(c.relpartbound, c.oid) AS bound
          FROM pg_inherits i
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_am a ON a.oid = c.relam
         WHERE i.inhparent = 'public.supplier_errors'::regclass
           AND a.amname <> 'heap'
         ORDER BY c.relname
    LOOP
        EXECUTE format('SELECT count(*) FROM public.%I', part.name) INTO v_rows;

        IF v_rows = 0 THEN
            -- 空壳通道（812 同款，含 TOCTOU 收口）。
            EXECUTE format('ALTER TABLE public.supplier_errors DETACH PARTITION public.%I', part.name);
            EXECUTE format('SELECT count(*) FROM public.%I', part.name) INTO v_rows;
            IF v_rows > 0 THEN
                EXECUTE format('ALTER TABLE public.supplier_errors ATTACH PARTITION public.%I %s',
                               part.name, part.bound);
                RAISE NOTICE '813: partition % raced non-empty during detach (% rows); re-attached, left untouched', part.name, v_rows;
                CONTINUE;
            END IF;
            EXECUTE format('DROP TABLE public.%I', part.name);
            EXECUTE format(
                'CREATE TABLE public.%I PARTITION OF public.supplier_errors %s',
                part.name, part.bound);
            RAISE NOTICE '813: rebuilt partition % as heap (was empty columnar)', part.name;
        ELSE
            -- 带数据通道（689/811 同款）：bak 改名 → 同边界重建 heap 叶 →
            -- 回拷 → 守恒校验（新 ≥ bak；并发 promote 落进新叶只会多不会
            -- 少）→ DROP bak。
            v_bak := part.name || '_am13_bak';
            EXECUTE format('ALTER TABLE public.supplier_errors DETACH PARTITION public.%I', part.name);
            EXECUTE format('ALTER TABLE public.%I RENAME TO %I', part.name, v_bak);
            EXECUTE format(
                'CREATE TABLE public.%I PARTITION OF public.supplier_errors %s',
                part.name, part.bound);
            EXECUTE format('INSERT INTO public.%I SELECT * FROM public.%I', part.name, v_bak);
            EXECUTE format('SELECT count(*) FROM public.%I', v_bak) INTO v_bak_rows;
            EXECUTE format('SELECT count(*) FROM public.%I', part.name) INTO v_new_rows;
            IF v_new_rows < v_bak_rows THEN
                RAISE EXCEPTION '813: parity check failed for %: bak=% new=% (rows lost?)',
                                part.name, v_bak_rows, v_new_rows;
            END IF;
            EXECUTE format('DROP TABLE public.%I', v_bak);
            RAISE NOTICE '813: converted % to heap (% rows moved)', part.name, v_bak_rows;
        END IF;
    END LOOP;
END $$;

-- ===========================================================================
-- 4. 落地自证：三件事必须同时成立，缺一件就大声失败
--
--    这一节存在的理由：813 之所以能「记账为已应用、活库却没变」，就是因为
--    没有任何一步会检查结果。**只有断言能关掉这个漏洞。**
-- ===========================================================================

DO $verify$
DECLARE
    v_def   text;
    v_n_bad integer;
    v_bad   text;
    v_clean text;
BEGIN
    -- (a) ensure 函数必须是 heap 版：活体不得再含 USING columnar
    v_def := pg_get_functiondef('public.ensure_supplier_errors_partition(timestamptz)'::regprocedure);
    IF v_def LIKE '%USING columnar%' THEN
        RAISE EXCEPTION '836 self-check failed: ensure_supplier_errors_partition still creates USING columnar partitions. The next ensure tick will push this family back to columnar, so converting the partitions alone is not a fix.';
    END IF;
    -- 正文里的 enforce_columnar_partition 只允许出现在注释里（813 的注释就提到
    -- 它），所以先剥掉行注释再查，而不是简单 LIKE。
    v_clean := regexp_replace(v_def, '--.*$', '', 'g');
    IF v_clean ~ 'enforce_columnar_partition' THEN
        RAISE EXCEPTION '836 self-check failed: ensure_supplier_errors_partition still calls enforce_columnar_partition in executable code (a comment mentioning it is fine).';
    END IF;

    -- (b) 一个 supplier_errors 分区都不许留在非 heap 访问方法上
    SELECT count(*), coalesce(string_agg(c.relname, ', '), '(none)')
      INTO v_n_bad, v_bad
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
      JOIN pg_am   a ON a.oid = c.relam
     WHERE i.inhparent = 'public.supplier_errors'::regclass
       AND a.amname <> 'heap';
    IF v_n_bad > 0 THEN
        RAISE EXCEPTION '836 self-check failed: % supplier_errors partition(s) are still not heap: %', v_n_bad, v_bad;
    END IF;

    -- (c) columnar_healthcheck 必须认识这一族，否则「漂移可见」这件事不成立
    IF pg_get_functiondef('public.columnar_healthcheck()'::regprocedure) !~ 'supplier_errors' THEN
        RAISE EXCEPTION '836 self-check failed: columnar_healthcheck() does not mention supplier_errors, so this family will keep reporting expected=''unknown'' and the drift stays invisible.';
    END IF;

    RAISE NOTICE '836 self-check: ensure function is heap-only, every supplier_errors partition is heap, and columnar_healthcheck() now knows this family.';
END
$verify$;

COMMIT;
