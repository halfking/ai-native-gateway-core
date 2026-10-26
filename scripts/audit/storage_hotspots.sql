-- =====================================================================
-- §143.91 存储热点审计（★ 只读；★ 可复现；★ 必须串行执行）
--
-- ★★★★★★ 为什么要有它（★ 本轮的目标）：
--   ★ 「存储与效率综合最优」这条命题，★ 第一步是【知道钱花在哪儿】。
--   ★ ⇒ ⇒ 而本轮实测发现两个量具级陷阱：
--     (1) ★★ `pg_stat_user_tables.n_live_tup` 会骗人
--         （★ 它是【采集快照】、★ 会过期 ⇒ ⇒ 「n_live_tup = 0」不等于空表）
--     (2) ★★ `pg_column_size(jsonb)` 返回的是【存储体积】不是【内容体积】
--         （★ 外部未压缩 TOAST 只返回 18 字节指针 ⇒ ⇒ 该口径【恒为下界】）
--   ★ ⇒ ⇒ 所以本脚本【必须】同时给：★ ① 表级体积 ② 实际行数 ③ reltuples 估算
--   ★ ⇒ ⇒ 三者并列，★ 才不会把「过期统计」读成「空表」；★
--   ★ ⇒ ⇒ 体积也必须【两口径并列】（★ 存储体积 + `length(::text)` 内容体积）。
--
-- ★ 用法（★ 在仓库根目录）：
--   bash scripts/audit/run_prod_sql.sh scripts/audit/storage_hotspots.sql 600 /private/tmp/p14391/hotspots
--
-- ★ ★★ 纪律：★ 只读；★ 不建任何对象（★ 连 TEMP 都不建）；★ 必须串行；
-- ★ ★★ 必须用真实退出码执行器（★ `| tail` 之后 `$?` 是 tail 的退出码）。
-- ★ ★★ 时区：★ 252/211 的 PG 会话时区是 Asia/Shanghai(+08)；
--   ★ 会话族分区键 `partition_date` 取 UTC 日历日 ⇒ ★ 两个口径不要混。
-- =====================================================================
SET statement_timeout = '300s';

\echo '=== 1/7: ★★★★★★ 表级存储 top 15（★ total = heap + toast + index）==='
SELECT schemaname,
       relname,
       n_live_tup,
       pg_size_pretty(pg_total_relation_size(relid)) AS total,
       pg_size_pretty(pg_relation_size(relid)) AS heap,
       pg_size_pretty(pg_total_relation_size(relid) - pg_relation_size(relid)) AS toast_and_idx,
       pg_total_relation_size(relid) AS total_bytes
  FROM pg_stat_user_tables
 ORDER BY pg_total_relation_size(relid) DESC
 LIMIT 15;

\echo '=== 2/7: ★★★★ reltuples 口径对账（★ n_live_tup 不可信 ⇒ 用 pg_class.reltuples）==='
-- ★ ★★ 口径：★ pg_stat_user_tables.n_live_tup 是【采集快照】⇒ ⇒ 会过期；
-- ★ ★★ 而 pg_class.reltuples 是【最近一次 ANALYZE/VACUUM】的估算。
SELECT c.relname,
       c.reltuples::bigint AS reltuples_est,
       c.relpages,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total
  FROM pg_class c
 WHERE c.relnamespace = 'public'::regnamespace
   AND c.relkind = 'r'
 ORDER BY pg_total_relation_size(c.oid) DESC
 LIMIT 15;

\echo '=== 3/7: ★★★★★★ 「n_live_tup = 0」的三张表：★ 必须独立 count（★ 独立 count，不是 UNION ALL）==='
-- ★ ★★ 本轮实测的三个反例（★ 全部不是空表！）：
--     session_turn_details_2026_09   n_live_tup=0 ⇒ 实际 786,036 行 / 603 MB
--     usage_ledger_2026_09           n_live_tup=0 ⇒ 实际 1,416,772 行 / 742 MB
--     ursm_node_snapshot_min_legacy  n_live_tup=0 ⇒ 实际 2,836,620 行 / 852 MB
-- ★ ★★ 逐条独立统计（★ 大表 count 放在同一分支里会互相拖累 ⇒ 不用 UNION ALL）。
SELECT count(*) AS real_rows FROM public.session_turn_details_2026_09;
SELECT count(*) AS real_rows FROM public.usage_ledger_2026_09;
SELECT count(*) AS real_rows FROM public.ursm_node_snapshot_min_legacy;

\echo '=== 4/7: ★★★★ reltuples / relpages 与体积（★ 前三张表的对照）==='
SELECT c.relname,
       c.relkind,
       c.reltuples::bigint AS reltuples_est,
       c.relpages,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total
  FROM pg_class c
 WHERE c.relname IN ('session_bodies_2026_10',
                     'session_turn_details_2026_09',
                     'usage_ledger_2026_09',
                     'ursm_node_snapshot_min_legacy')
 ORDER BY c.relname;

\echo '=== 5/7: ★★★★★★ 会话正文族按 kind 拆解（★ 判定 5,228 MB 里有多少是 final_full）==='
-- ★ ★★ 口径提醒：★ `pg_column_size` 是【存储体积】（★ 压缩后）⇒ ⇒ 【恒为内容体积的下界】。
-- ★ ★★ ★ 而 `length(outbound_body::text)` 是【内容真实体积】（★ 会 detoast ⇒ 会慢）。
SELECT kind,
       count(*) AS rows,
       sum(pg_column_size(outbound_body)) AS toast_bytes,
       max(pg_column_size(outbound_body)) AS max_toast
  FROM public.session_bodies_unified
 GROUP BY kind
 ORDER BY toast_bytes DESC;

\echo '=== 6/7: ★★★★ final_full 快照的体积分布（★ 注意：★ 该口径【低估】）==='
SELECT count(*) AS n_rows,
       sum(pg_column_size(outbound_body)) AS toast_total,
       avg(pg_column_size(outbound_body))::bigint AS avg_toast,
       percentile_cont(0.5)  WITHIN GROUP (ORDER BY pg_column_size(outbound_body))::bigint AS p50,
       percentile_cont(0.9)  WITHIN GROUP (ORDER BY pg_column_size(outbound_body))::bigint AS p90,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY pg_column_size(outbound_body))::bigint AS p99,
       max(pg_column_size(outbound_body)) AS max_toast
  FROM public.session_bodies_unified
 WHERE kind = 'final_full';

\echo '=== 7/7: ★★★★ 单个长驻会话的正文画像（★ §143.90 登记的那个）==='
-- ★ ★★ 用 `length(::text)` 而不是 `pg_column_size`：★ 后者是【存储体积】、★ 恒为下界。
SELECT kind,
       count(*) AS rows,
       sum(pg_column_size(outbound_body)) AS toast_bytes,
       sum(length(outbound_body::text)) AS text_bytes,
       max(length(outbound_body::text)) AS max_text_bytes,
       min(ts) AS first_ts,
       max(ts) AS last_ts
  FROM public.session_bodies_unified
 WHERE session_id = 'gw_omp-global-kxpms'
 GROUP BY kind
 ORDER BY kind;
-- =====================================================================
