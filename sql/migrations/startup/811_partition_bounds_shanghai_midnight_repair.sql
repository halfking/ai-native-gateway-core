-- ===========================================================================
-- File:          sql/migrations/startup/811_partition_bounds_shanghai_midnight_repair.sql
-- Migration:     811
-- Database:      llm_gateway
-- Purpose:       修复 request_logs / routing_decision_log 分区边界的
--                473 型 UTC 零点污染（687 的姊妹篇；R20 SQL 日志审计轮）
--
-- Status:        active
-- Idempotent:    YES（干净分区与已重建分区按边界文本跳过；bak 清扫通道兜底）
-- Dependencies:  687_fix_473_partition_0800_bounds.sql（同病史同通道；
--                687 在本机执行时 request_logs 仅 default 分区、且当时
--                252 台账未登记，故 252 生产库的污染保留至今）
--
-- Background:
--   正典约定（694 头注）：生产分区边界 = Asia/Shanghai 零点；ensure 函数
--   `SET LOCAL TIME ZONE 'Asia/Shanghai'` 后用裸日期 %L 字面量建下月分区。
--   252 生产库实测（2026-10-02）：request_logs / routing_decision_log 的
--   2026_09/2026_10 分区边界是 UTC 零点（pg_get_expr 渲染为
--   '...T08:00:00+08'）——473 型污染（或 10-01 并行轨道列存转换重建时
--   以 UTC 会话传参）。后果：
--
--     ERROR: partition "routing_decision_log_2026_11" would overlap
--            partition "routing_decision_log_2026_10"
--            (ensure 裸日期 '2026-11-01' = +08 零点 < 存量上界 08:00+08)
--
--   ensure 自 10-01 15:05 起每小时失败（overlap ×8/18.5h），2026_11 永远
--   建不出来。两表在 252 均无 DEFAULT 分区 ⇒ 2026-11-01 08:00 CST 起
--   所有 inserts 将硬失败（no partition found）——30 天时间炸弹。
--
-- 通道（687 先例的两遍改造）：
--   第一遍：凡边界文本含 '08:00:00' 的月分区 → DETACH → 改名 *_bak →
--   按正典 +08 零点重建（存储引擎跟随原分区：routing_decision_log
--   为正典单族 columnar，request_logs 为 heap；父表分区索引对新分区
--   自动建叶）；第二遍：bak 按月升序经父表回灌（跨月边界的行按 ts
--   自动路由到相邻新分区）→ 计数守恒校验 → DROP bak。
--   末尾调用正典 ensure 函数预建下月分区（功能验证 + 立即解除炸弹）。
-- ===========================================================================

BEGIN;

SET LOCAL statement_timeout = '30min';
SET LOCAL lock_timeout = '120s';

DO $$
DECLARE
    r record;
    v_month text;
    v_from text;
    v_to text;
    v_bak text;
    v_using text;
    v_bak_rows bigint;
    v_moved bigint;
    v_beyond bigint;
    v_next text;
BEGIN
    -- 第一遍：重建全部污染分区（此阶段不动数据，bak 暂存）
    FOR r IN
        SELECT p.relname::text AS parent,
               c.relname::text AS part,
               am.amname::text AS am,
               pg_get_expr(c.relpartbound, c.oid) AS bounds
          FROM pg_inherits i
          JOIN pg_class p ON p.oid = i.inhparent
          JOIN pg_class c ON c.oid = i.inhrelid
          JOIN pg_am am ON am.oid = c.relam
         WHERE p.relname IN ('request_logs', 'routing_decision_log')
           AND c.relispartition
         ORDER BY p.relname, c.relname
    LOOP
        CONTINUE WHEN position('08:00:00' in r.bounds) = 0;

        v_month := regexp_replace(r.part, '^.*_([0-9]{4}_[0-9]{2})$', '\1');
        IF v_month = r.part THEN
            RAISE EXCEPTION '811: partition % has 08:00 bounds but non-monthly name', r.part;
        END IF;
        v_from := to_char(to_date(v_month, 'YYYY_MM'), 'YYYY-MM-DD') || ' 00:00:00+08';
        v_to   := to_char(to_date(v_month, 'YYYY_MM') + interval '1 month', 'YYYY-MM-DD') || ' 00:00:00+08';
        v_bak  := r.part || '_bounds_repair_bak';
        v_using := CASE WHEN r.am = 'columnar' THEN ' USING columnar' ELSE '' END;

        EXECUTE format('ALTER TABLE public.%I DETACH PARTITION public.%I', r.parent, r.part);
        EXECUTE format('ALTER TABLE public.%I RENAME TO %I', r.part, v_bak);
        EXECUTE format(
            'CREATE TABLE public.%I PARTITION OF public.%I FOR VALUES FROM (%L) TO (%L)%s',
            r.part, r.parent, v_from, v_to, v_using);
        RAISE NOTICE '811: rebuilt %.% with bounds [% , %)', r.parent, r.part, v_from, v_to;
    END LOOP;

    -- 第二遍：bak 按月升序经父表回灌（跨月行按 ts 路由到相邻新分区）
    FOR r IN
        SELECT n.nspname::text AS schema,
               c.relname::text AS bak,
               regexp_replace(regexp_replace(c.relname, '_bounds_repair_bak$', ''),
                              '^.*_([0-9]{4}_[0-9]{2})$', '\1') AS month
          FROM pg_class c
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'public'
           AND c.relname LIKE '%_bounds_repair_bak'
           AND c.relkind = 'r'
         ORDER BY regexp_replace(c.relname, '_bounds_repair_bak$', '')
    LOOP
        v_month := r.month;
        v_to := to_char(to_date(v_month, 'YYYY_MM') + interval '1 month', 'YYYY-MM-DD') || ' 00:00:00+08';
        EXECUTE format('SELECT count(*) FROM public.%I', r.bak) INTO v_bak_rows;
        EXECUTE format('SELECT count(*) FROM public.%I WHERE ts >= %L',
                       r.bak, v_to) INTO v_beyond;
        IF v_beyond > 0 THEN
            -- 污染边界吞进的次月头 8 小时行：经父表路由进已重建的次月分区
            v_next := regexp_replace(regexp_replace(r.bak, '_bounds_repair_bak$', ''),
                                     '_[0-9]{4}_[0-9]{2}$', '');
            v_next := v_next || '_' || to_char(to_date(v_month, 'YYYY_MM') + interval '1 month', 'YYYY_MM');
            IF to_regclass('public.' || v_next) IS NULL THEN
                RAISE EXCEPTION '811: % holds % rows beyond % and next-month partition % does not exist; manual channel required',
                                r.bak, v_beyond, v_to, v_next;
            END IF;
            RAISE NOTICE '811: % holds % rows beyond %; routing them into next-month partition %',
                         r.bak, v_beyond, v_to, v_next;
        END IF;
        EXECUTE format('INSERT INTO public.%I SELECT * FROM public.%I',
                       regexp_replace(regexp_replace(r.bak, '_bounds_repair_bak$', ''),
                                      '_[0-9]{4}_[0-9]{2}$', ''), r.bak);
        GET DIAGNOSTICS v_moved = ROW_COUNT;
        IF v_moved <> v_bak_rows THEN
            RAISE EXCEPTION '811: % conservation violated: bak=% moved=%', r.bak, v_bak_rows, v_moved;
        END IF;
        EXECUTE format('DROP TABLE public.%I', r.bak);
        RAISE NOTICE '811: replayed % rows from % and dropped bak', v_moved, r.bak;
    END LOOP;

    -- 解除炸弹的功能验证：预建下月分区（正典 ensure 函数，+08 零点语义）
    PERFORM public.ensure_request_logs_partition(now() + interval '1 month');
    PERFORM public.ensure_routing_decision_log_partition(now() + interval '1 month');
END $$;

COMMIT;
