-- =====================================================================
-- §143.94 session_turns 族【逐列拆解 + 大对象来源定位】只读复算脚本
--
-- ★★★★★★ 为什么要有它（★ 本轮的核心发现）：
--   ★ session_turns 族 4,446 MB（★ §143.91 登记的第三大未审存储块）
--   ★ ⇒ 而 9 月分区的 2,867,789,824 B 里，★ 两个 jsonb 列就占 38.5%：
--   ★   compression_meta 569,396,910 B ＋ digest 533,160,775 B
--   ★ ⇒ ⇒ 根因 ＝ 【compression_meta 里的 sanitize_message_refs 是 256 条
--   ★   【每条含两个 64-hex sha256】的坐标表，★ 而它【每轮被整体重写一遍】】
--   ★ ⇒ ⇒ digest 则是【管理员安全摘要】的正文（★ user_input / assistant_output
--   ★   各截断 260 runes）⇒ ⇒ 533 MB 是【设计使然】、不是缺陷
--   ★ ⇒ ⇒ ⇒ 本脚本必须能【逐条复现】这三件事，否则结论无法复算。
--
-- ★ 用法（★ 在仓库根目录）：
--   bash scripts/audit/run_prod_sql.sh scripts/audit/verify_session_turns_column_decomposition.sql 900 /private/tmp/p14394/cols
--
-- ★ ★★ 纪律：★ 只读；★ 不建任何对象（★ 连 TEMP 都不建）；★ 必须串行；
-- ★ ★★ 必须用真实退出码执行器（★ `| tail` 之后 `$?` 是 tail 的退出码）。
--
-- ★ ★★★ 六条已知踩坑（★ 别重试）：
--   ① 【全表 count(*) 与多列 count(DISTINCT ...) 都会超时】—— ★ 9 月分区 80 万行
--      且带 TOAST ⇒ 600s 跑不完 ⇒ ⇒ 必须【单表】/【限定窗口】/【按 id 采样】；
--   ② ★★ 【jsonb 比较会 detoast】—— ★ `compression_meta <> '{}'::jsonb`
--      在全分区上直接超时 ⇒ ⇒ 内容级判据只能【抽样】；
--   ③ ★★ 【`pg_column_size(digest)` 在 `digest IS NULL` 判定上也要 detoast】
--      ⇒ ⇒ 判空必须用 CASE 短路（★ 先判 IS NULL 再算 size）；
--   ④ `pg_column_size(jsonb)` 是【存储体积】不是【内容体积】⇒ ⇒ 恒为下界；
--   ⑤ `pg_class` 没有 `relid` 列（★ 用 `c.oid`）；★ `pg_inherits` 的父子列是
--      `inhrelid` / `inhparent`；
--   ⑥ ★★ 【孤儿表判定不能一刀切】—— ★ 父表本身也不在 `pg_inherits` 里
--      ⇒ ⇒ 必须【逐个列名点名】。
-- =====================================================================
SET statement_timeout = '600s';

\echo '=== 1/8: ★★★★★★ 族清单：★ 父表下 vs 孤儿（★ 逐个列名点名）==='
SELECT c.relname AS relname,
       c.relkind AS relkind,
       CASE WHEN c.relkind = 'p' THEN 'parent-table'
            WHEN i.inhrelid IS NULL THEN 'ORPHAN-not-under-parent'
            ELSE 'under-parent' END AS attach_state,
       c.reltuples::bigint AS reltuples_est,
       pg_total_relation_size(c.oid) AS total_bytes
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  LEFT JOIN pg_inherits i ON i.inhrelid = c.oid
 WHERE n.nspname = 'public'
   AND c.relname LIKE 'session_turns%'
   AND c.relkind IN ('r','p')
 ORDER BY pg_total_relation_size(c.oid) DESC;

\echo '=== 2/8: ★★★★★★ 9 月分区的【逐列】体积分解（★ 不许只测一列代表整行）==='
SELECT count(*) AS rows,
       sum(pg_column_size(compression_meta)) AS compression_meta_bytes,
       sum(pg_column_size(tools))            AS tools_bytes,
       sum(pg_column_size(digest))           AS digest_bytes,
       sum(pg_column_size(multimodal_types)) AS multimodal_bytes,
       sum(length(coalesce(title,'')))       AS title_bytes,
       sum(length(coalesce(summary,'')))     AS summary_bytes,
       sum(length(coalesce(model,'')))        AS model_bytes,
       sum(length(coalesce(provider,'')))     AS provider_bytes,
       sum(length(coalesce(error_kind,'')))   AS error_kind_bytes
  FROM public.session_turns_2026_09;

\echo '=== 3/8: ★★★★ compression_meta 的【存储体积】直方图（★ 只用 pg_column_size ⇒ 不 detoast）==='
SELECT CASE WHEN pg_column_size(compression_meta) = 0 THEN '00_zero'
            WHEN pg_column_size(compression_meta) < 64 THEN '01_lt64'
            WHEN pg_column_size(compression_meta) < 1024 THEN '02_64to1k'
            WHEN pg_column_size(compression_meta) < 8192 THEN '03_1kto8k'
            WHEN pg_column_size(compression_meta) < 65536 THEN '04_8kto64k'
            ELSE '05_ge64k' END AS bucket,
       count(*) AS rows,
       sum(pg_column_size(compression_meta)) AS bytes,
       max(pg_column_size(compression_meta)) AS max_bytes
  FROM public.session_turns_2026_09
 GROUP BY 1
ORDER BY 1;

\echo '=== 4/8: ★★★★★★ 哪一列是主体：★ 按 strategy 拆 compression_meta（★ 不用 jsonb 列）==='
SELECT compression_strategy AS strategy,
       count(*) AS rows,
       sum(pg_column_size(compression_meta)) AS meta_bytes,
       round(avg(pg_column_size(compression_meta))::numeric,1) AS avg_bytes,
       min(ts) AS min_ts, max(ts) AS max_ts
  FROM public.session_turns_2026_09
 GROUP BY 1
ORDER BY meta_bytes DESC;

\echo '=== 5/8: ★★★★★★ 大对象的规模：★ 有多少会话、多少行、占多少字节 ==='
WITH big AS (
  SELECT session_id, count(*) AS turns, sum(pg_column_size(compression_meta)) AS meta_bytes
    FROM public.session_turns_2026_09
   WHERE pg_column_size(compression_meta) > 8192
   GROUP BY session_id
)
SELECT count(*) AS big_sessions,
       sum(turns) AS big_rows,
       sum(meta_bytes) AS big_meta_bytes,
       pg_size_pretty(sum(meta_bytes)) AS pretty,
       round(avg(turns)::numeric,1) AS avg_turns_per_session
  FROM big;

\echo '=== 6/8: ★★★★★★ 逐轮重写：★ 同一会话相邻两轮的 meta 体积（★ 是不是每轮重写同一份）==='
WITH s AS (
  SELECT session_id, turn_no, compression_strategy,
         pg_column_size(compression_meta) AS cur_bytes,
         lag(pg_column_size(compression_meta)) OVER (PARTITION BY session_id ORDER BY turn_no) AS prev_bytes
    FROM public.session_turns_2026_09
   WHERE id IN (
     SELECT id FROM public.session_turns_2026_09
      WHERE pg_column_size(compression_meta) > 8192
      ORDER BY id DESC LIMIT 200
   )
)
SELECT session_id, turn_no, compression_strategy, cur_bytes, prev_bytes
  FROM s
 WHERE session_id = (SELECT session_id FROM s WHERE cur_bytes > 8192 GROUP BY session_id ORDER BY count(*) DESC LIMIT 1)
 ORDER BY turn_no
 LIMIT 30;

\echo '=== 7/8: ★★★★ digest 列的【存储体积】直方图（★ 判空用 CASE 短路 ⇒ 不 detoast）==='
SELECT CASE WHEN digest IS NULL THEN '00_null'
            WHEN pg_column_size(digest) = 0 THEN '01_zero'
            WHEN pg_column_size(digest) < 1024 THEN '02_1k'
            WHEN pg_column_size(digest) < 8192 THEN '03_1kto8k'
            WHEN pg_column_size(digest) < 65536 THEN '04_8kto64k'
            ELSE '05_ge64k' END AS bucket,
       count(*) AS rows,
       sum(pg_column_size(digest)) AS bytes,
       max(pg_column_size(digest)) AS max_bytes
  FROM public.session_turns_2026_09
 GROUP BY 1
ORDER BY 1;

\echo '=== 8/8: ★★★★★★ 10 月是否还在产生大对象（★ 按 id 倒序采样，★ 避开全分区扫）==='
SELECT compression_strategy AS strategy,
       count(*) AS rows,
       sum(pg_column_size(compression_meta)) AS meta_bytes,
       round(avg(pg_column_size(compression_meta))::numeric,1) AS avg_bytes,
       min(ts) AS min_ts, max(ts) AS max_ts
  FROM (
    SELECT compression_strategy, compression_meta, ts
      FROM public.session_turns_2026_10
     ORDER BY id DESC
     LIMIT 20000
  ) t
 GROUP BY 1
ORDER BY meta_bytes DESC;
-- =====================================================================
