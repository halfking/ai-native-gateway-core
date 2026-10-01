-- ===========================================================================
-- File:          sql/migrations/startup/810_heap_partitions_toastless_heal.sql
-- Migration:     810
-- Database:      llm_gateway
-- Purpose:       治愈「heap 但无 TOAST 表」的空分区（R20 SQL 日志审计轮）
--
-- Status:        active
-- Idempotent:    YES（治愈后扫描为空集，零动作；非空分区 NOTICE 指路跳过）
-- Dependencies:  806_session_bodies_partitions_heap.sql（同通道先例；
--                806 的谓词 relam<>'heap' 恰好漏掉本迁移针对的
--                "已是 heap 但 reltoastrelid=0"形态）
--
-- Background:
--   2026-10-01 列存事故的回退留下了一批「heap 但没有 TOAST 表」的分区：
--   columnar 表天然无 TOAST，`ALTER TABLE ... SET ACCESS METHOD heap` 的
--   原地重写路径不重建 TOAST 关系；列定义的 attstorage 仍标 'x'
--   （可 TOAST），于是任何压缩后仍超 8160B 的行 INSERT 即炸：
--
--     ERROR:  row is too big: size 36112, maximum size 8160
--
--   生产实证（252，docs/audit/2026-10-02-252-sql-log-audit-round20.md）：
--   session_bodies_2026_10（空分区被并行轨道往返实验后遗留 toastless）
--   自 10-01 16:06 起 promote 每小时批全灭（row-too-big ×59/14h），
--   session_bodies_hot 积压 4,227 行 14h 无出口（数据安全，滞留 hot）。
--
--   全库扫描（2026-10-02）共 21 个 heap-without-toast 分区：
--   - 空分区（本迁移治愈，零数据风险）：session_bodies_{2026_10,default}、
--     sessions_{2026_07,2026_08,2026_11,default}、session_turns_default、
--     auto_route_selections_{2026_10,default} 等；
--   - 非空分区（行均为小行、从未触发，按 806 同款约束保持原样并
--     NOTICE 指路）：usage_facts_default(274k)、usage_facts 各日分区、
--     stats_event_inbox_default(78k)、sessions_2026_09/10、
--     session_censors_2026_09、session_memora_2026_09。
--     这些表的行都是小元数据行（≤8160B），无近期触发面；根治待各自的
--     月分区自然轮换到「空」状态时由本迁移的幂等重放兜底。
--
-- 治愈通道 = 806 先例：DETACH + DROP + 按原边界重建（heap 新建自带
-- TOAST）+ session_bodies 家族补 lz4 压缩（765 C 段同款五列）。
-- ===========================================================================

BEGIN;

DO $$
DECLARE
    part record;
    v_rows bigint;
    col text;
BEGIN
    FOR part IN
        SELECT c.oid::regclass::text AS qualified_name,
               (SELECT n.nspname || '.' || p.relname
                  FROM pg_inherits i
                  JOIN pg_class p ON p.oid = i.inhparent
                  JOIN pg_namespace n ON n.oid = p.relnamespace
                 WHERE i.inhrelid = c.oid) AS parent,
               pg_get_expr(c.relpartbound, c.oid) AS bound
          FROM pg_class c
          JOIN pg_am am ON am.oid = c.relam
         WHERE am.amname = 'heap'
           AND c.reltoastrelid = 0
           AND c.relispartition
           AND EXISTS (SELECT 1 FROM pg_attribute a
                        WHERE a.attrelid = c.oid AND a.attnum > 0
                          AND NOT a.attisdropped
                          AND a.attstorage IN ('x', 'm', 'e'))
         ORDER BY 1
    LOOP
        EXECUTE format('SELECT count(*) FROM %s', part.qualified_name) INTO v_rows;
        IF v_rows > 0 THEN
            RAISE NOTICE '810: % is heap-without-toast and NON-EMPTY (% rows); left untouched (manual channel: 806/562 rebuild)', part.qualified_name, v_rows;
            CONTINUE;
        END IF;

        EXECUTE format('ALTER TABLE %s DETACH PARTITION %s', part.parent, part.qualified_name);
        EXECUTE format('DROP TABLE %s', part.qualified_name);
        EXECUTE format('CREATE TABLE %s PARTITION OF %s %s',
                       part.qualified_name, part.parent, part.bound);

        IF part.parent LIKE '%.session_bodies' THEN
            FOREACH col IN ARRAY ARRAY['request_delta', 'response_delta', 'outbound_body',
                                       'request_attachments', 'response_attachments']
            LOOP
                EXECUTE format('ALTER TABLE %s ALTER COLUMN %I SET COMPRESSION lz4',
                               part.qualified_name, col);
            END LOOP;
        END IF;

        RAISE NOTICE '810: rebuilt % as heap-with-toast', part.qualified_name;
    END LOOP;
END $$;

COMMIT;
