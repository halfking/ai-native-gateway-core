-- Migration 689: candidate_failure_logs 月度分区 columnar → heap(保数据转换)
--                + ensure 函数去 columnar + drop_old_state_partitions 按表 TTL
--
-- Background(2026-09-09 24h 审计第三轮 P1 缺口 #1/#2):
--   candidate_failure_logs 是分区父表,写入走 candidate_failure_logs_hot,
--   经 promote 链入月度分区(392/535/628)。392 起
--   ensure_candidate_failure_logs_partition() 创建 `USING columnar` 月度
--   分区,幂等分支还会调用 enforce_columnar_partition() 强制 columnar。
--   Columnar 分区 append-only(562 迁移已确立该结论),导致:
--
--   1. 行级 DELETE 必败:bg/opslog_trimmer.go 对 candidate_failure_logs 的
--      `DELETE ... WHERE ts < NOW()-$1` 打在 columnar 分区上必失败,且错误
--      只 slog.Warn 被吞 → lifecycle.candidate_failure_logs_ttl_days(7d)
--      行删路径完全失效。本迁移把所有 columnar 分区转成 heap,DELETE 恢复。
--   2. drop 侧失效:bg/partition_manager.go dropOldStatePartitions 取所有
--      state 表 TTL 的 max 且 floor 30d,candidate_failure_logs 分区要等
--      ≥30d 才可能 drop,与其 7d 语义脱节。本迁移新增按表 TTL 函数
--      drop_old_state_partition_table(parent, days),Go 侧逐表调用。
--   3. ensure 函数替换:CREATE OR REPLACE 为 heap 版本(逐字参照 392,仅去
--      `USING columnar` 子句与 ELSE 分支的 enforce_columnar_partition 调用),
--      防止 689 之后 ensure 又建出 columnar 分区。
--
-- 与 562 的区别:562 只处理"空分区"(有数据的跳过);本迁移面对的分区
-- 可能有数据(实测 2026_09 有 3.4 万行),必须保数据转换:
--   对每个 columnar 分区:
--     DETACH → 旧表改名 *_col2heap_bak → 按原 relpartbound 逐字重建同名
--     heap 分区(ATTACH)→ INSERT SELECT 拷回 → 行数守恒校验 → DROP bak。
--   整个迁移单事务:任一步失败整体回滚,数据无损(bak 仍在)。
--
-- enforce_columnar_partition(text, text) 本身不动:它还被
-- columnar_insert_only_parents()(routing_decision_log)的 event trigger
-- 与 supplier_errors(V371)等多表使用。
--
-- 幂等:是(已是 heap 的分区直接跳过;函数均为 CREATE OR REPLACE)。
-- Down: 无(660+ 惯例,转换不可逆;columnar 化无业务收益)。

BEGIN;

-- 252/154 shared PG enforces statement_timeout=30s by default; the
-- columnar→heap data-move INSERT SELECT (26281+ rows on the
-- candidate_failure_logs_2026_09 partition during the 2026-09-09 audit)
-- blew past 30s on the production cluster and aborted the whole
-- transaction. Same idiom as 632/649: raise it for this transaction only.
SET LOCAL statement_timeout = '10min';

-- ═══════════════════════════════════════════════════════════════
-- 1. 替换 ensure_candidate_failure_logs_partition 为 heap 版本
-- ═══════════════════════════════════════════════════════════════
-- 函数体逐字参照 392,仅去掉 `USING columnar` 子句,以及 ELSE 分支的
-- enforce_columnar_partition 调用(改为无需处理)。保持 RETURNS text
-- 签名与返回值语义(promote 链 535/628 依赖返回分区名)。

CREATE OR REPLACE FUNCTION public.ensure_candidate_failure_logs_partition(target_ts timestamp with time zone)
RETURNS text
LANGUAGE plpgsql
AS $$
DECLARE
    month_start    date := date_trunc('month', target_ts)::date;
    month_end      date := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name text := 'candidate_failure_logs_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- 689: heap (was columnar). Row-level DELETE (the 7d TTL trim path
        -- in bg/opslog_trimmer.go) and the hot→monthly promote chain both
        -- need UPDATE/DELETE-capable storage; columnar partitions are
        -- append-only (established by migration 562).
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF candidate_failure_logs
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_candidate_failure_logs_partition: created % as heap', partition_name;
    END IF;
    -- 689: dropped the former ELSE-branch enforce_columnar_partition() call —
    -- partitions are heap now and must stay heap.
    RETURN partition_name;
END;
$$;

COMMENT ON FUNCTION ensure_candidate_failure_logs_partition(timestamp with time zone) IS
'Ensure monthly partition for candidate_failure_logs (heap since 689).
Changed from columnar to heap by Migration 689 (2026-09-09): columnar
partitions are append-only, which broke the 7d TTL row-level DELETE path
(bg/opslog_trimmer.go) and the hot→monthly promote chain.';

-- ═══════════════════════════════════════════════════════════════
-- 2. 按表 TTL 的分区 DROP 函数 + drop_old_state_partitions 改写为包装
-- ═══════════════════════════════════════════════════════════════
-- 391 的 drop_old_state_partitions(retention_days) 对 5 张 state 表用
-- 同一个 retention;Go 侧为不误删而取各表 TTL 的 max 且 floor 30d,
-- candidate_failure_logs 的 7d 语义因此脱节。新增单表函数,由 Go 逐表
-- 传入各自的 TTL;drop_old_state_partitions 保持原签名与语义
-- (单一 retention 应用到全部 5 张表)以兼容既有运维调用。

CREATE OR REPLACE FUNCTION public.drop_old_state_partition_table(
    p_parent_name text,
    p_retention_days integer
)
RETURNS bigint
LANGUAGE plpgsql
AS $function$
DECLARE
    total_dropped bigint := 0;
    cutoff_ts timestamptz;
    r record;
    partition_name text;
    partition_year int;
    partition_month int;
    partition_first_day date;
BEGIN
    IF p_retention_days < 1 THEN
        RAISE WARNING 'drop_old_state_partition_table: retention_days=% < 1, clamping to 1', p_retention_days;
        p_retention_days := 1;
    END IF;
    cutoff_ts := NOW() - (p_retention_days || ' days')::interval;

    FOR r IN
        SELECT c.relname AS partname
        FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE p.relname = p_parent_name
          AND c.relname ~ ('^' || p_parent_name || '_\d{4}_\d{2}$')
    LOOP
        partition_name := r.partname;
        -- Parse YYYY_MM from suffix (e.g. "candidate_failure_logs_2026_07")
        BEGIN
            partition_year := split_part(partition_name, '_', array_length(string_to_array(partition_name, '_'), 1) - 1)::int;
            partition_month := split_part(partition_name, '_', array_length(string_to_array(partition_name, '_'), 1))::int;
            partition_first_day := make_date(partition_year, partition_month, 1);

            -- DROP if entire month is before cutoff
            IF (partition_first_day + INTERVAL '1 month' - INTERVAL '1 day') < cutoff_ts THEN
                EXECUTE format('DROP TABLE IF EXISTS %I', partition_name);
                total_dropped := total_dropped + 1;
                RAISE DEBUG 'drop_old_state_partition_table: dropped %', partition_name;
            END IF;
        EXCEPTION WHEN OTHERS THEN
            RAISE WARNING 'drop_old_state_partition_table: failed to parse % (%)', partition_name, SQLERRM;
            -- Continue with next partition, don't abort the whole loop
        END;
    END LOOP;

    RETURN total_dropped;
END;
$function$;

COMMENT ON FUNCTION drop_old_state_partition_table(text, int) IS
    'Drops monthly partitions older than the given retention for ONE parent
table (per-table TTL). Added by migration 689 so candidate_failure_logs can
drop at its own 7d TTL while other state tables keep 30d. Used by
bg.partition_manager. Idempotent.';

CREATE OR REPLACE FUNCTION public.drop_old_state_partitions(p_retention_days int DEFAULT 30)
RETURNS bigint
LANGUAGE plpgsql
AS $function$
DECLARE
    total_dropped bigint := 0;
    target_tables text[] := ARRAY[
        'routing_decision_log',
        'candidate_failure_logs',
        'handoff_logs',
        'model_probe_runs',
        'credential_model_index'
    ];
    parent_name text;
BEGIN
    IF p_retention_days < 1 THEN
        RAISE WARNING 'drop_old_state_partitions: retention_days=% < 1, clamping to 1', p_retention_days;
        p_retention_days := 1;
    END IF;

    -- 689: delegating to the per-table helper keeps the original
    -- single-retention semantics (backwards compatible) while the Go
    -- scheduler can now call drop_old_state_partition_table() directly
    -- with each table''s own TTL.
    FOREACH parent_name IN ARRAY target_tables LOOP
        total_dropped := total_dropped + drop_old_state_partition_table(parent_name, p_retention_days);
    END LOOP;

    RETURN total_dropped;
END;
$function$;

COMMENT ON FUNCTION drop_old_state_partitions(int) IS
    'Drops monthly partitions older than the given retention for state/routing
tables (single retention applied to all tables). Since 689 this delegates to
drop_old_state_partition_table(); bg.partition_manager now calls the per-table
function directly with each table''s own lifecycle TTL. Idempotent.';

-- ═══════════════════════════════════════════════════════════════
-- 3. 所有 columnar 月度分区 → heap(保数据转换,逐个处理)
-- ═══════════════════════════════════════════════════════════════
-- pg_class.relam = 'columnar' access method 的分区逐个:
--   DETACH → 改名 bak → 按原 bound 重建同名 heap 分区 → 拷回数据 →
--   行数守恒校验 → DROP bak。已是 heap 的分区跳过(幂等)。
-- 任一步失败 RAISE EXCEPTION,整个迁移事务回滚,bak 表随事务回滚恢复,
-- 数据无损。

DO $$
DECLARE
    r record;
    v_part   text;
    v_bak    text;
    v_bound  text;
    v_src    bigint;
    v_dst    bigint;
    v_moved  bigint;
BEGIN
    FOR r IN
        SELECT c.relname::text AS part,
               pg_get_expr(c.relpartbound, c.oid) AS bound
          FROM pg_class c
          JOIN pg_am am ON am.oid = c.relam
          JOIN pg_namespace n ON n.oid = c.relnamespace
          JOIN pg_inherits i ON i.inhrelid = c.oid
          JOIN pg_class p ON p.oid = i.inhparent
         WHERE n.nspname = 'public'
           AND p.relname = 'candidate_failure_logs'
           AND am.amname = 'columnar'
         ORDER BY c.relname
    LOOP
        v_part  := r.part;
        v_bound := r.bound;
        v_bak   := v_part || '_col2heap_bak';

        IF v_bound IS NULL THEN
            RAISE EXCEPTION '689: % has no partition bound — manual investigation needed', v_part;
        END IF;

        EXECUTE format('SELECT count(*) FROM public.%I', v_part) INTO v_src;

        RAISE NOTICE '689: converting % (storage=columnar, rows=%, bound=%)',
                     v_part, v_src, v_bound;

        -- 3.1 摘下来并让位(数据留在 bak 表,失败可随事务回滚)
        EXECUTE format('ALTER TABLE public.candidate_failure_logs DETACH PARTITION public.%I', v_part);
        EXECUTE format('ALTER TABLE public.%I RENAME TO %I', v_part, v_bak);

        -- 3.2 按原 bound 逐字重建同名 heap 分区(列定义/约束随 ATTACH 继承)
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.candidate_failure_logs %s',
            v_part, v_bound);

        -- 3.3 拷回数据
        EXECUTE format('INSERT INTO public.%I SELECT * FROM public.%I', v_part, v_bak);
        GET DIAGNOSTICS v_moved = ROW_COUNT;
        EXECUTE format('SELECT count(*) FROM public.%I', v_part) INTO v_dst;

        IF v_dst <> v_src OR v_moved <> v_src THEN
            RAISE EXCEPTION '689: row conservation FAILED for % (src=%, moved=%, dst=%) — aborting, transaction will roll back',
                            v_part, v_src, v_moved, v_dst;
        END IF;

        -- 3.4 校验通过后才删 bak
        EXECUTE format('DROP TABLE public.%I', v_bak);

        RAISE NOTICE '689: converted % to heap, rows conserved (%)', v_part, v_dst;
    END LOOP;

    IF NOT FOUND THEN
        RAISE NOTICE '689: no columnar partitions found under candidate_failure_logs — nothing to convert';
    END IF;
END $$;

-- ═══════════════════════════════════════════════════════════════
-- 4. 验证:分区全 heap、全部 attached、无遗留 bak 表
-- ═══════════════════════════════════════════════════════════════

DO $$
DECLARE
    v_columnar int;
    v_total    int;
    v_detached int;
    v_baks     int;
BEGIN
    SELECT count(*) INTO v_columnar
      FROM pg_class c
      JOIN pg_am am ON am.oid = c.relam
      JOIN pg_namespace n ON n.oid = c.relnamespace
      JOIN pg_inherits i ON i.inhrelid = c.oid
      JOIN pg_class p ON p.oid = i.inhparent
     WHERE n.nspname = 'public'
       AND p.relname = 'candidate_failure_logs'
       AND am.amname = 'columnar';

    SELECT count(*) INTO v_total
      FROM pg_inherits i
      JOIN pg_class p ON p.oid = i.inhparent
      JOIN pg_namespace n ON n.oid = p.relnamespace
      JOIN pg_class c ON c.oid = i.inhrelid
     WHERE n.nspname = 'public'
       AND p.relname = 'candidate_failure_logs';

    -- 只看月度命名(_YYYY_MM)的表,排除 _hot / _columnar_archive 等非分区表
    SELECT count(*) INTO v_detached
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public'
       AND c.relname ~ '^candidate_failure_logs_\d{4}_\d{2}$'
       AND c.relkind = 'r'
       AND NOT EXISTS (
           SELECT 1 FROM pg_inherits i
            WHERE i.inhrelid = c.oid
              AND i.inhparent = 'public.candidate_failure_logs'::regclass);

    SELECT count(*) INTO v_baks
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public'
       AND c.relname LIKE 'candidate_failure_logs_%_col2heap_bak';

    IF v_columnar > 0 THEN
        RAISE EXCEPTION '689: VERIFY FAIL — % columnar partition(s) remain', v_columnar;
    END IF;
    IF v_baks > 0 THEN
        RAISE EXCEPTION '689: VERIFY FAIL — % leftover _col2heap_bak table(s)', v_baks;
    END IF;
    IF v_detached > 0 THEN
        RAISE EXCEPTION '689: VERIFY FAIL — % candidate_failure_logs table(s) detached from parent', v_detached;
    END IF;

    RAISE NOTICE '689: VERIFY PASS — % partition(s) all heap, all attached, no bak leftovers', v_total;
END $$;

COMMIT;

-- ═══════════════════════════════════════════════════════════════
-- 部署后人工验证步骤
-- ═══════════════════════════════════════════════════════════════
--
-- 1. 分区存储全部 heap:
--    SELECT c.relname, am.amname FROM pg_class c
--      JOIN pg_am am ON am.oid = c.relam
--      JOIN pg_inherits i ON i.inhrelid = c.oid
--      JOIN pg_class p ON p.oid = i.inhparent
--     WHERE p.relname = 'candidate_failure_logs';
--
-- 2. 行级 DELETE 生效(7d TTL 路径恢复):
--    DELETE FROM candidate_failure_logs WHERE ts < now() - interval '400 days';
--
-- 3. ensure 新建分区为 heap:
--    SELECT ensure_candidate_failure_logs_partition(now() + interval '1 month');
