-- ===========================================================================
-- File:          sql/migrations/startup/806_session_bodies_partitions_heap.sql
-- Migration:     806
-- Database:      llm_gateway
-- Purpose:       session_bodies 既有 columnar 月分区转 heap（708 的前置）
--
-- Status:        active
-- Idempotent:    YES (逐分区检查 relam 与行数，非 heap 且空才动作)
-- Dependencies:  708_session_bodies_s1a.sql（其后执行，须先就位）
--                562_fix_request_logs_bodies_partitions_heap.sql（同族先例）
--
-- Background:
--   01-schema baseline 在 columnar 默认访问方法区间内预建了
--   session_bodies_2026_07/2026_08（fresh-install e2e 实证两者 relam=
--   columnar）；生产真库的 session_bodies 全部分区均为 heap（promote
--   ON CONFLICT 与列存互斥，ensure 家族早已改建 heap）。708 的 kind
--   回填对每个既有分区逐条 UPDATE——ColumnarScan 不支持 UPDATE，序列
--   在 708:121 必炸；其后的 final_full 部分唯一索引（递归建叶）同样
--   无法在列存分区上落地。
--
--   处方镜像 562（request_logs_bodies 同族先例）：仅对「非 heap 且为空」
--   的分区 DETACH + DROP + 按原边界重建 heap；非空的列存分区按 562 的
--   load-bearing 约束保持原样并 NOTICE 指路。存量库重放=分区已是 heap
--   →循环为空集，零动作。
-- ===========================================================================

BEGIN;

DO $$
DECLARE
    part record;
    v_rows bigint;
BEGIN
    FOR part IN
        SELECT c.relname AS name,
               pg_get_expr(c.relpartbound, c.oid) AS bound
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_am a ON a.oid = c.relam
        WHERE i.inhparent = 'public.session_bodies'::regclass
          AND a.amname <> 'heap'
        ORDER BY c.relname
    LOOP
        EXECUTE format('SELECT count(*) FROM public.%I', part.name) INTO v_rows;
        IF v_rows = 0 THEN
            EXECUTE format('ALTER TABLE public.session_bodies DETACH PARTITION public.%I', part.name);
            EXECUTE format('DROP TABLE public.%I', part.name);
            EXECUTE format(
                'CREATE TABLE public.%I PARTITION OF public.session_bodies %s',
                part.name, part.bound);
            RAISE NOTICE '806: rebuilt partition % as heap', part.name;
        ELSE
            RAISE NOTICE '806: partition % is non-heap and non-empty; left untouched (see 562 load-bearing ruling)', part.name;
        END IF;
    END LOOP;
END $$;

COMMIT;
