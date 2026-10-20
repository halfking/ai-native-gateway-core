-- =====================================================================
-- verify_reclaim_candidates.sql —— 「可回收对象」判定的事实层（只读）
--
-- 生成时间：2026-10-21（§143.42 轮）
-- 目的：把「这张表是空的 / 这张表是孤儿 / 这张表可以回收」三个断言
--       从「靠 pg_stat_user_tables.n_live_tup 猜」改成「精确取事实」。
--
-- ─────────────────────────────────────────────────────────────────────
-- ★★★ 三条量具纪律（本脚本的存在理由，也是 2026-10-21 §143.40 误判的来源）
--
-- ① 判空【必须】用 EXISTS(SELECT 1 FROM t LIMIT 1) 或精确 count(*)。
--    ⇒ 禁止用 pg_stat_user_tables.n_live_tup 判空。
--    原因（252 实测）：n_live_tup 对「长期无写入 + 从未 analyze」的表恒为 0。
--      §143.40 用它把 20 个「零行对象」列进回收包 ⇒ 实测 17 个其实有行
--      （ursm_node_snapshot_min_legacy 2,836,620 行 / session_title_states 175,975 行 /
--        9 月「空分区包」11 个全部非空）⇒ 照单执行会跨 17 张表销毁 300 万+ 行。
--
-- ② 判「孤儿分区」【必须】用 pg_class.relispartition。
--    ⇒ 禁止用「pg_inherits 里没有父」当孤儿证据。
--    原因：普通表（relkind='r'，非分区）**永远**没有 pg_inherits 记录
--      ⇒ 「不在父表下」对普通表是恒真的空命题 ⇒ §143.40 的 4 个「orphan」
--      实测 relispartition 全为 f ⇒ 它们全是普通表，不是被 DETACH 的分区。
--      真正的孤儿分区判据 = relispartition = true AND pg_inherits 无父记录。
--      ★ 252 实测：全库 relispartition=true 且无父的对象 = 0 个。
--
-- ③ 判「回收了多少字节」【必须】看磁盘，不看 catalog 汇总。
--    ⇒ TRUNCATE 后 pg_relation_size() 仍可能报告旧 TOAST 文件的大小。
--    ⇒ 判「回收了多少」用 du / pg_stat_file，或回收后再量一次。
--
-- ─────────────────────────────────────────────────────────────────────
-- ★ 本脚本【只读】：只查 pg_catalog，不建对象、不写数据、不加锁到业务表。
--   用法：psql -f verify_reclaim_candidates.sql
--   ⚠ 大表的 count(*) 可能很慢；本脚本【不】做 count(*)，只做 EXISTS 判空
--     （EXISTS + LIMIT 1 是 O(1)，且对「是否为空」是精确的）。
-- =====================================================================

\pset pager off
\timing off

SET statement_timeout = '120s';
SET lock_timeout = '5s';
SET work_mem = '32MB';

-- ─────────────────────────────────────────────────────────────────────
-- 事实表：public schema 里所有 > 10 MB 的关系（分区与普通表都算）
--
-- ★ 候选集【不】按「孤儿」筛 —— 按「大小」筛，把事实交给判定层。
--   （筛选口径本身就是 §143.40 的错误来源之一：先筛「看起来没用」再取事实。）
-- ─────────────────────────────────────────────────────────────────────
WITH facts AS (
    SELECT
        c.oid,
        c.relname,
        c.relkind,
        c.relispartition,                                    -- ② 孤儿判据
        pg_get_expr(c.relpartbound, c.oid) AS partbound,     -- ② DEFAULT 判据
        (SELECT count(*) FROM pg_inherits i WHERE i.inhrelid = c.oid) AS n_parent,
        (SELECT count(*) FROM pg_inherits i WHERE i.inhparent = c.oid) AS n_child,
        pg_total_relation_size(c.oid)::bigint AS total_bytes,
        pg_relation_size(c.oid)::bigint AS heap_bytes,
        pg_indexes_size(c.oid)::bigint AS index_bytes,
        (
            SELECT count(*) FROM pg_proc p
             JOIN pg_namespace pn ON pn.oid = p.pronamespace
             WHERE pn.nspname = 'public'
               AND p.prosrc ILIKE '%' || c.relname || '%'
        ) AS func_refs
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public'
      AND c.relkind IN ('r', 'p')
      AND pg_total_relation_size(c.oid) > 10 * 1024 * 1024
)
SELECT
    f.relname,
    f.total_bytes,
    f.relkind,
    f.relispartition,
    f.n_parent,
    f.n_child,
    f.partbound,
    f.heap_bytes,
    f.index_bytes,
    f.func_refs
FROM facts f
ORDER BY f.total_bytes DESC;

-- ─────────────────────────────────────────────────────────────────────
-- 空判定函数：给任意关系名返回精确的「是否为空」。
--
-- ★ 为什么单独做成函数：判定层（Go 侧决策表）需要**逐个对象**的
--   精确空判定，而把 EXISTS 写进一个 VALUES 列表在对象名很多时不可读。
--   本函数是 pg_temp ⇒ 会话结束即消失 ⇒ 对生产零残留。
--
-- ★ 用法（判定层）：reclaim_verdict(regclass) → TEXT
--   返回形如 'RECLAIMABLE_TRUNCATE|VACUUM_FULL|理由' / 'NOT_RECLAIMABLE|-|理由'
--   决策表在 Go 侧（admin/reclaim_verdict.go）并由
--   admin/section_14342_reclaim_measurement_test.go 的决策表自测钉住。
-- ─────────────────────────────────────────────────────────────────────
CREATE OR REPLACE FUNCTION pg_temp.reclaim_is_empty(p_rel regclass)
RETURNS boolean
LANGUAGE plpgsql
AS $$
DECLARE
    v_has boolean;
    v_q    text;
BEGIN
    IF p_rel IS NULL THEN
        RETURN false;
    END IF;
    v_q := format('SELECT EXISTS (SELECT 1 FROM %s LIMIT 1)', p_rel::text);
    EXECUTE v_q INTO v_has;
    RETURN NOT v_has;
END;
$$;

COMMENT ON FUNCTION pg_temp.reclaim_is_empty(regclass) IS
    '§143.42：精确判空探针（形态见脚本头纪律①）。禁用统计估算量具。';
