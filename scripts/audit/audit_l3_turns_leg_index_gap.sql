-- =====================================================================
-- §143.100 只读审计脚本：L3 冷启动回退 turns 腿的【索引缺口】
--
-- ★ 本轮要回答的问题（承接 §143.99 的长尾定位）：
--   §143.99 实测：L3 回退的 turns 腿在 4 万轮次 session 上单次要 24,763 ms，
--   而 EXPLAIN 显示它走的是 `(tenant_id, ts)` 索引 + **Filter: session_id**。
--   ⇒ ⇒ 【同一个 session 的 42,289 行里过滤出 40,381 行】⇒ ⇒ 过滤率 95.5%
--   ⇒ ⇒ ⇒ 成本 = 42,289 次回堆。
--
-- ★ ★★ 核心问题：**为什么 planner 不走那条「精确」的 `(session_id, turn_no DESC)` 索引？**
--   实测答案（EXPLAIN）：★ 走 turn_no 索引虽然能精确 Index Cond，
--   ★ 但**排序键不匹配**（ORDER BY ts ≠ 索引序）⇒ ⇒ planner 只能选
--   ★ 「能出 ts 序但要回堆过滤」的那条 ⇒ ⇒ ⇒ **缺的是一条 `(session_id, ts DESC)`。**
--
-- ★ ★★ 本脚本给出的可判定结论：
--   ① 索引缺口：★ turns 分区族**没有** `(session_id, ts DESC)` 形态的索引
--      ⇒ ⇒ ⇒ **这是零语义变更的修法**（只加索引，不改 SQL 形态）。
--   ② 反证：**换排序键（ts DESC → turn_no DESC）虽然快 4,762×，但不是语义等价**
--      ⇒ ⇒ 实测秩不一致率 93.9%、最大秩差 12,482 ⇒ ⇒ ⇒ **不许那样改**。
--   ③ 现时速率：★ 两次采样取增量 ⇒ ⇒ 「累计 6,819 s」里有多少是「现在还在发生」。
--
-- ★ 只读：★ 本脚本【不建任何对象、不写任何行】⇒ ⇒ 全部语句以 SELECT 开头。
--   ★ 生产护栏拒绝行首的写/DDL 关键字。
--
-- 用法（★ 必须串行；★ 生产 252 共享库）：
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/audit_l3_turns_leg_index_gap.sql 900
--   ★ 现时速率需要两次运行（★ 间隔 ≥10 分钟）⇒ ⇒ 用仓内 diff_l3_snapshots.py 差分。
-- =====================================================================
SET statement_timeout = '300s';
SET lock_timeout = '10s';

\echo '=== Q1 量具自检 —— rows 语义锚点（恒空⇒0；count(*)⇒1；generate_series(1,3)⇒3） ==='
SELECT 1 AS anchor_zero_rows WHERE false;
SELECT count(*) AS anchor_one_row FROM (SELECT 1 WHERE false) x;
SELECT g AS anchor_three_rows FROM generate_series(1, 3) g;

\echo '=== Q2 turns 分区族的 session_id 打头索引（★ 重点：有没有 (session_id, ts DESC)） ==='
SELECT c.oid::regclass::text AS rel,
       i.relname            AS idx,
       pg_get_indexdef(x.indexrelid) AS idxdef
FROM pg_index x
JOIN pg_class c ON c.oid = x.indrelid
JOIN pg_class i ON i.oid = x.indexrelid
WHERE c.relname LIKE 'session_turns%'
  AND pg_get_indexdef(x.indexrelid) LIKE '%session_id%'
  AND pg_get_indexdef(x.indexrelid) NOT LIKE '%turn_no%'
  AND pg_get_indexdef(x.indexrelid) NOT LIKE '%CASE%'
ORDER BY rel, idx;

\echo '=== Q3 ★ 索引缺口自检 —— turns 族有没有 (session_id, ts DESC) ==='
SELECT count(*) FILTER (
         WHERE pg_get_indexdef(x.indexrelid) LIKE '%(session_id%'
           AND pg_get_indexdef(x.indexrelid) LIKE '%ts DESC%') AS idx_session_id_ts_desc,
       count(*) FILTER (
         WHERE pg_get_indexdef(x.indexrelid) LIKE '%(session_id, turn_no DESC)%') AS idx_session_id_turn_no,
       count(DISTINCT c.oid) FILTER (
         WHERE c.relname LIKE 'session_turns_2%') AS monthly_partitions
FROM pg_index x
JOIN pg_class c ON c.oid = x.indrelid
WHERE c.relname LIKE 'session_turns%';

\echo '=== Q4 ★ 计划对照（纯 EXPLAIN，不带 ANALYZE）★ 四个形态 ==='
-- ★ 取一个「轮次最多」的 session 作参数（★ 探针参数来源必须与判据对齐）。
SELECT session_id AS probe_session, tenant_id AS probe_tenant, count(*) AS turns
FROM public.session_turns_hot
WHERE session_id IS NOT NULL AND session_id <> '' AND tenant_id <> ''
GROUP BY session_id, tenant_id
ORDER BY turns DESC
LIMIT 1 \gset

\echo '--- Q4-A: 当前形态（ORDER BY ts DESC LIMIT 1）★ 基线 ---'
EXPLAIN
SELECT t.request_id
FROM public.session_turns t
WHERE t.session_id = :'probe_session'
  AND t.tenant_id = :'probe_tenant'
ORDER BY t.ts DESC
LIMIT 1;

\echo '--- Q4-B: 换排序键（ORDER BY turn_no DESC LIMIT 1）★ ★ 快，但语义不等价（见 Q5） ---'
EXPLAIN
SELECT t.request_id
FROM public.session_turns t
WHERE t.session_id = :'probe_session'
  AND t.tenant_id = :'probe_tenant'
ORDER BY t.turn_no DESC
LIMIT 1;

\echo '--- Q4-C: 只加轮次上限（ORDER BY ts DESC LIMIT 50）★ ★ 计划不变 ⇒ 修不了 ---'
EXPLAIN
SELECT t.request_id
FROM public.session_turns t
WHERE t.session_id = :'probe_session'
  AND t.tenant_id = :'probe_tenant'
ORDER BY t.ts DESC
LIMIT 50;

\echo '--- Q4-D: 只换排序键不加上限（ORDER BY turn_no DESC LIMIT 1）★ 对照 ---'
EXPLAIN
SELECT t.request_id
FROM public.session_turns t
WHERE t.session_id = :'probe_session'
  AND t.tenant_id = :'probe_tenant'
ORDER BY t.turn_no DESC
LIMIT 1;

\echo '=== Q5 ★ 语义等价性反证 —— 会话内 turn_no 与 ts 的秩一致率 ==='
-- ★ 结论（实测）：★ 40,381 行里 37,917 行秩不一致（93.9%），最大秩差 12,482
-- ★ ⇒ ⇒ 「换排序键」会取到【不同的那一轮】⇒ ⇒ **不是等价优化**。
SELECT count(*)                                                AS n,
       count(*) FILTER (WHERE ts_rn <> turn_no_rn)             AS rank_mismatch,
       round(100.0 * count(*) FILTER (WHERE ts_rn <> turn_no_rn)
             / NULLIF(count(*), 0), 4)                          AS mismatch_pct,
       max(abs(ts_rn - turn_no_rn))                            AS max_rank_diff
FROM (SELECT turn_no,
             row_number() OVER (PARTITION BY session_id ORDER BY ts DESC)     AS ts_rn,
             row_number() OVER (PARTITION BY session_id ORDER BY turn_no DESC) AS turn_no_rn
      FROM public.session_turns_2026_09
      WHERE session_id = 'sys:probe:cred35:20260923') x;

\echo '=== Q6 探测型 session 的轮次累积（★ 长尾的来源） ==='
SELECT CASE WHEN session_id LIKE 'sys:probe:%' THEN 'sys:probe:*'
            WHEN session_id LIKE 'sys:%'        THEN 'sys:*'
            ELSE 'gw:*' END AS session_kind,
       count(DISTINCT session_id) AS sessions,
       count(*)                   AS turns,
       max(c)                     AS max_turns_in_session
FROM (SELECT session_id, count(*) AS c
      FROM public.session_turns_2026_09
      WHERE session_id IS NOT NULL AND session_id <> ''
      GROUP BY session_id) t
GROUP BY 1
ORDER BY turns DESC;

\echo '=== Q7 ★ 现时速率快照（★ 两次运行取增量 ⇒ diff_l3_snapshots.py） ==='
SELECT 'L3_turns_leg_snapshot' AS tag,
       queryid,
       calls,
       round(total_exec_time::numeric, 1) AS total_exec_ms,
       round(mean_exec_time::numeric, 2)  AS mean_exec_ms,
       round(max_exec_time::numeric, 1)  AS max_exec_ms,
       rows,
       round((shared_blks_hit + shared_blks_read)::numeric * 8192
             / GREATEST(calls, 1))         AS bytes_per_call,
       (query LIKE '%outbound_body <> %::jsonb%') AS is_gamma_form,
       (query LIKE '%LATERAL%')                   AS has_lateral
FROM pg_stat_statements
WHERE dbid = (SELECT dbid FROM pg_stat_database WHERE datname = current_database())
  AND query LIKE '%session_bodies_hot%'
  AND query LIKE '%outbound_body%'
  AND query LIKE 'SELECT %'
ORDER BY total_exec_time DESC;
