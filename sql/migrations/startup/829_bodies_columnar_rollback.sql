-- ===========================================================================
-- File:          sql/migrations/startup/829_bodies_columnar_rollback.sql
-- Migration:     829
-- Database:      llm_gateway
-- Purpose:        **止血** —— 让 request_logs_bodies 回到全 heap，并保证此后
--                新建的月分区不再自动变成 columnar。
--
-- 立项依据（本机库实测，非推断）：
--
-- 1. 故障不是 `request_logs_bodies` 专属，而是 Citus `columnar` 的性质。
--    审计 §9.197.4：7 个「列存分区 + `_hot` 孪生」的母表，两腿 `UNION ALL`
--    放进未命名子查询时 **7/7 全部**计划失败
--    （`invalid perminfoindex 0 in RTE with relid 0`）；
--    而同样是两面的全 heap `request_logs` 同形状正常返回 2,184,300 行。
--    §9.197.2 的 21 种形状对照还钉住了边界：顶层 `UNION ALL`、CTE、
--    普通/嵌套子查询、子查询内 JOIN、子查询内 `UNION`(去重)/`EXCEPT` 都**安全**——
--    触发条件是「列存关系 + **子查询内的 UNION ALL**」，不是「子查询不能包列存表」。
--
-- 2. **回滚 765 不足以止血。** 765 的 A 段把
--    `ensure_request_logs_bodies_partition` 重定义为「有 `citus_columnar` 扩展
--    就用 `USING columnar` 建月分区」。只要这个函数还在，
--    **新分区会继续被建成列存**。所以本迁移的头号动作是**重定义该函数为恒 heap**。
--
-- 3. **不需要 3 GB 全表重写。** 765 的作者当初就写了
--    「仅转空分区（数据安全阀）……非空分区不动，按 TTL 整分区 DROP 退役」，
--    本迁移**沿用同一姿态**：
--      · `2026_11`（0 行）  → 直接转 heap，零代价；
--      · `2026_10`（25,675 行 / 23 MB）→ 有数据，**不重写**，走 TTL；
--      · `2026_09`（2,219,097 行 / 3,026 MB）→ 有数据，**不重写**，走 TTL。
--    而 `drop_old_request_logs_bodies_partitions()` 对列存分区做的是**裸
--    `DROP TABLE`**（实测其函数体，无数据搬迁、无 UPDATE/DELETE 路径要求），
--    `lifecycle.request_logs_bodies_ttl_days` 默认 **7 天**
--    （settings/spec_lifecycle.go，HotReload），由
--    `bg/partition_manager.go:1232` 周期调用
--    ⇒ `2026_09` 的月末是 2026-10-01，**自 2026-10-08 起会被自动 DROP**。
--    换言之：**不做任何重写，3 GB 也会在数日内自然消失。**
--
-- 范围（属主已拍板，决策表 D30-a）：**只回 765 管的 request_logs_bodies**。
-- 另外 6 个列存族（`routing_decision_log` 268 MB、`credential_model_index` 30 MB、
-- `handoff_logs`、`supplier_errors`、`usage_ledger`、`request_wal`，合计约 333 MB）
-- **不在本迁移范围内**——它们各有各的迁移历史，须单独决策。
--
-- 幂等：CREATE OR REPLACE FUNCTION / IF EXISTS / DO 守卫，可安全重放。
-- ===========================================================================
BEGIN;

-- ── 1. ensure 函数：恒 heap，不再按扩展存在与否分支 ────────────────────────
-- 保留函数签名与返回类型不变，只去掉 columnar 分支。
CREATE OR REPLACE FUNCTION public.ensure_request_logs_bodies_partition(
    target_ts timestamp with time zone DEFAULT now()
) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start    date;
    month_end      date;
    partition_name text;
BEGIN
    SET LOCAL TIME ZONE 'Asia/Shanghai';
    month_start := date_trunc('month', target_ts)::date;
    month_end   := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name := 'request_logs_bodies_' || to_char(month_start, 'YYYY_MM');
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs_bodies
             FOR VALUES FROM (%L) TO (%L)',
            partition_name, month_start, month_end);
        RAISE NOTICE 'ensure_request_logs_bodies_partition: created % as heap', partition_name;
    END IF;
END;
$$;

COMMENT ON FUNCTION public.ensure_request_logs_bodies_partition(timestamp with time zone) IS
    '创建 request_logs_bodies 月分区，**恒用 heap**。829 起去掉了 columnar 分支：'
    '列存关系一旦出现在未命名子查询里参与 UNION ALL，计划期就抛 '
    'invalid perminfoindex 0 in RTE with relid 0（审计 §9.197）。';

-- ── 2. 存量**空**列存月分区 → heap ────────────────────────────────────────
-- 与 765 的 B 段同一姿态：**只转空分区**。先查空、取父表 ACCESS EXCLUSIVE
-- 后**复核**（防 promote 竞态），再 DROP + 重建为 heap。
DO $$
DECLARE
    part        record;
    is_empty    boolean;
    month_start date;
    month_end   date;
BEGIN
    FOR part IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_inherits i ON i.inhrelid = c.oid
        JOIN pg_am am ON am.oid = c.relam
        WHERE i.inhparent = 'request_logs_bodies'::regclass
          AND am.amname = 'columnar'
          AND c.relname ~ '^request_logs_bodies_[0-9]{4}_[0-9]{2}$'
    LOOP
        EXECUTE format('SELECT NOT EXISTS (SELECT 1 FROM public.%I LIMIT 1)', part.relname)
            INTO is_empty;
        IF NOT is_empty THEN
            RAISE NOTICE '829: % has rows, keep as-is (retires via TTL partition drop, no rewrite)', part.relname;
            CONTINUE;
        END IF;
        month_start := to_date(substring(part.relname FROM '([0-9]{4}_[0-9]{2})$'), 'YYYY_MM');
        month_end   := (month_start + interval '1 month')::date;
        LOCK TABLE public.request_logs_bodies IN ACCESS EXCLUSIVE MODE;
        -- 持锁后复核空：与检查窗口内落进来的行互斥
        EXECUTE format('SELECT NOT EXISTS (SELECT 1 FROM public.%I LIMIT 1)', part.relname)
            INTO is_empty;
        IF NOT is_empty THEN
            RAISE NOTICE '829: % received rows during window, keep as-is', part.relname;
            CONTINUE;
        END IF;
        EXECUTE format('DROP TABLE public.%I', part.relname);
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF public.request_logs_bodies
             FOR VALUES FROM (%L) TO (%L)',
            part.relname, month_start, month_end);
        RAISE NOTICE '829: converted empty % from columnar to heap', part.relname;
    END LOOP;
END;
$$;

-- ── 3. 留一份可查的现状清单（不删任何数据） ────────────────────────────────
-- 便于部署后核对「哪些列存分区还在、还有多少行、靠 TTL 何时退场」。
-- drop_old_request_logs_bodies_partitions() 自 2026-10-08 起会逐月清掉有数据的那些。
DO $$
DECLARE
    r         record;
    total_mb  bigint;
BEGIN
    total_mb := 0;
    FOR r IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_inherits i ON i.inhrelid = c.oid
        JOIN pg_am am ON am.oid = c.relam
        WHERE i.inhparent = 'request_logs_bodies'::regclass
          AND am.amname = 'columnar'
        ORDER BY c.relname
    LOOP
        total_mb := total_mb + (pg_total_relation_size(r.relname::regclass) / 1048576);
        RAISE NOTICE '829: still columnar (no rewrite; will be DROPped by TTL) — %', r.relname;
    END LOOP;
    IF total_mb > 0 THEN
        RAISE NOTICE '829: % MB of columnar partitions left, all data-bearing. '
                     'No rewrite performed on purpose: drop_old_request_logs_bodies_partitions() '
                     'issues a plain DROP TABLE on them, which needs no data movement '
                     '(audit §9.200.3).', total_mb;
    END IF;
END;
$$;

COMMIT;
