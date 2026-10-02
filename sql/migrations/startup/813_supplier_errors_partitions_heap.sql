-- ===========================================================================
-- File:          sql/migrations/startup/813_supplier_errors_partitions_heap.sql
-- Migration:     813
-- Database:      llm_gateway
-- Purpose:       supplier_errors 族 AM 归一 heap（R21 SQL 日志审计轮）：
--                四列存分区转 heap + ensure 函数去 columnar 化（断自愈反噬环）
--
-- Status:        active
-- Idempotent:    YES（重跑循环空集；函数 CREATE OR REPLACE）
-- Dependencies:  699（被本迁移运行期取代）/ 689（drop 通道不变）/ 812（同通道先例）
--
-- Background（R20 §五.2 裁决 + R21 取证，2026-10-02）：
--
-- 1. R20 登记的「supplier_errors 90d TTL 是 Row-level DELETE、11-06 必炸」
--    经代码+真库复核为误登记：
--      - bg/opslog_trimmer.go 只管 candidate_failure_logs 与
--        credential_probe_model_log，无 supplier_errors 路径；
--      - 全仓 supplier_errors 的 row-level DELETE 仅存在于 _hot（promote
--        批内 DELETE FROM supplier_errors_hot，heap）；
--      - supplier_errors 的 TTL 走 bg/partition_manager.go
--        stateTableTTLSpecs → drop_old_state_partition_table（689 helper），
--        整分区 DROP TABLE（252 真库函数体指纹核验：
--        has_drop_table=t / has_row_delete=f），对列存分区安全。
--        supplier_errors_2026_08 的出窗条件（month_end-1d < now-90d）在
--        2026-11-29 后首个 day-2 tick（≈2026-12-02）触发，非 11-06。
--
-- 2. 尽管无 TTL 炸弹，列存残留仍是正典单族基线（R18：仅
--    {routing_decision_log}，765 加 request_logs_bodies 三分区）之外的
--    10-01 事故漂移，带两类潜伏面：
--      a) UPDATE/DELETE 计划毒源（㊿ 查询引用面）：任何打到本族分区的
--         UPDATE/DELETE 计划（含 NOT EXISTS/FROM 臂）即炸 CTID 错误；
--         无 ts 谓词且投影 tableoid 的读路径同样炸
--         （2026-10-02 实测：SELECT tableoid,count(*) GROUP BY 1 →
--         "UPDATE and CTID scans not supported for ColumnarScan"，
--         而普通 count(*) 129,279 行可读）。
--      b) 自愈反噬环：ensure_supplier_errors_partition 的 ELSE 分支每小时
--         ensure tick 调 enforce_columnar_partition 把既有分区强行转回
--         columnar——只转分区不换函数，漂移必然复发（689 换函数+转分区
--         两步缺一不可的机制原因）。
--
-- 3. 本迁移两步齐做（689 同款）：
--      第一步  CREATE OR REPLACE ensure_supplier_errors_partition 为 heap
--              版（去 USING columnar、去 enforce 调用；上海钉扎保留——
--              baseline_ensure_functions_contract_test 钉的第一语句）；
--      第二步  现存四个列存分区转 heap：空分区（2026_08/2026_11，实测
--              0 行）走 812 同款 DETACH+锁内二次 count+DROP+重建；
--              非空分区（2026_09=124,846 行 / 2026_10=4,433 行，10-02
--              实测）走 689/811 同款带数据通道：DETACH→改名 bak→按原
--              边界重建同名 heap 叶→INSERT SELECT 回拷→行数守恒校验
--              （新行数 ≥ bak 行数，容忍并发 promote 写入）→DROP bak。
--
--   并发窗口说明：DETACH→重建之间（秒级）若 promote tick 恰好 INSERT，
--   656 模板式单语句 CTE 整批原子失败、行留在 supplier_errors_hot、
--   下一小时 tick 重试——与 810 对 session_bodies_2026_10 的取舍一致。
--   事件触发器反噬已排除：columnar_insert_only_parents() 252 实测=
--   ARRAY['routing_decision_log']（正典单族），不含 supplier_errors；
--   函数先换再转分区，转换期间不会旧函数复活 enforce。
--
--   fresh-install 不需本迁移：三基线的 ensure 函数同步改 heap 版
--   （sql/schema/01-schema.sql、deploy/sql/schemas/baseline/01-schema.sql、
--   installer embeddata/01-schema.sql，与迁移体逐字一致），新环境从源头
--   建 heap。历史迁移 699 文件本体不动（revision-sequence 不可变纪律）。
-- ===========================================================================

BEGIN;

SET LOCAL statement_timeout = '10min';
-- 811-R29 追认同款：pg_get_expr 渲染的分区边界文本随会话时区变化，
-- 钉扎 +08 使重建边界与日志/台账指纹在同一渲染口径下可审计。
SET LOCAL TIME ZONE 'Asia/Shanghai';

-- ═══════════════════════════════════════════════════════════════
-- 1. ensure_supplier_errors_partition → heap 版（先换函数，断反噬环）
--    体与三基线同步后的版本逐字一致（三基线一致性契约测试钉同形）。
-- ═══════════════════════════════════════════════════════════════

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

-- ═══════════════════════════════════════════════════════════════
-- 2. 现存列存分区转 heap（空壳 812 通道 / 带数据 689 通道）
-- ═══════════════════════════════════════════════════════════════

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

COMMIT;
