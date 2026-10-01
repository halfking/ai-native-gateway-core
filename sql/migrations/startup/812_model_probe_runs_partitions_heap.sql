-- ===========================================================================
-- File:          sql/migrations/startup/812_model_probe_runs_partitions_heap.sql
-- Migration:     812
-- Database:      llm_gateway
-- Purpose:       model_probe_runs 空列存分区转 heap（R20 SQL 日志审计轮；
--                CTID ×114 家族的真根因修复）
--
-- Status:        active
-- Idempotent:    YES（转后循环空集）
-- Dependencies:  806_session_bodies_partitions_heap.sql（同通道先例）
--
-- Background:
--   bg 探针状态更新器（`UPDATE model_probe_state ... NOT EXISTS (SELECT 1
--   FROM model_probe_runs ...)`）自 2026-10-01 11:36 起间歇性失败：
--
--     ERROR: UPDATE and CTID scans not supported for ColumnarScan
--     （×114/18.5h，~6/h；R19 轮曾误归因于 model_probe_state 自身 AM）
--
--   真根因：model_probe_runs 的 2026_09/2026_10/default 分区在 10-01
--   列存事故中被转 columnar（不在 R18 正典单族 {routing_decision_log}
--   内=漂移残留）。citus columnar 拒绝任何含 ColumnarScan 节点的
--   UPDATE 计划——即使 UPDATE 目标（model_probe_state）是 heap、即使
--   列存分区为空（分区剪枝按边界不按行数，created_at>now()-2h 恒命中
--   当月分区）。间歇性=计划在 hot 臂/月分区臂之间的剪枝抖动。
--
--   实测（2026-10-02）：三个月分区全为 0 行空壳（活数据在独立双写表
--   model_probe_runs_hot=heap）。806 同款处方：仅空分区 DETACH+DROP+
--   重建 heap；非空列存分区保持原样并 NOTICE 指路。转后更新器计划
--   不再含 ColumnarScan，永久愈合。
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
         WHERE i.inhparent = 'public.model_probe_runs'::regclass
           AND a.amname <> 'heap'
         ORDER BY c.relname
    LOOP
        EXECUTE format('SELECT count(*) FROM public.%I', part.name) INTO v_rows;
        IF v_rows = 0 THEN
            EXECUTE format('ALTER TABLE public.model_probe_runs DETACH PARTITION public.%I', part.name);
            EXECUTE format('DROP TABLE public.%I', part.name);
            EXECUTE format(
                'CREATE TABLE public.%I PARTITION OF public.model_probe_runs %s',
                part.name, part.bound);
            RAISE NOTICE '812: rebuilt partition % as heap', part.name;
        ELSE
            RAISE NOTICE '812: partition % is non-heap and non-empty; left untouched (manual channel: 806/562 rebuild)', part.name;
        END IF;
    END LOOP;
END $$;

COMMIT;
