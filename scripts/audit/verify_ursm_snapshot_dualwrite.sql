-- =====================================================================
-- §143.93 ursm 快照族【双写 + 孤儿表】只读复算脚本
--
-- ★★★★★★ 为什么要有它（★ 本轮的核心发现）：
--   ★ 生产上这张表 9,852 MB（★ 父表下 9,000 MB + 孤儿 legacy 852 MB）
--   ★ ⇒ 而 252 共享库上【每分钟有 2 个快照批次】、★ 相邻两批【内容逐字段相同】
--   ★ ⇒ ⇒ 根因 = 【两个实例的 60s tick 相位错开 ⇒ 互斥锁永不冲突 ⇒ 两边都写】
--   ★ ⇒ ⇒ 而【互斥锁不是去重锁】⇒ ⇒ §61 的跨实例写入者守卫【挡不住这个形态】
--   ★ ⇒ ⇒ ⇒ 本脚本必须能【逐条复现】这三件事，否则结论无法复算。
--
-- ★ 用法（★ 在仓库根目录）：
--   bash scripts/audit/run_prod_sql.sh scripts/audit/verify_ursm_snapshot_dualwrite.sql 900 /private/tmp/p14393/dual
--
-- ★ ★★ 纪律：★ 只读；★ 不建任何对象（★ 连 TEMP 都不建）；★ 必须串行；
-- ★ ★★ 必须用真实退出码执行器（★ `| tail` 之后 `$?` 是 tail 的退出码）。
--
-- ★ ★★★ 六条已知踩坑（★ 别重试）：
--   ① 【全表 count(*) 会超时】—— ★ 9 张日分区合计 3,200 万行 ⇒ 600s 跑不完
--      ⇒ ⇒ 必须【单表】或【限定时间窗】⇒ ⇒ ★★ 「读数超时」本身就是一条结论
--      （★ 那张表的体量已经大到不能全扫了）；
--   ② `count(DISTINCT (a,b,c))` 同样超时（★ 多列 DISTINCT 走排序）；
--      ⇒ ⇒ 需要「唯一内容数」时，改用【固定小时间窗】；
--   ③ `md5(string_agg(... ORDER BY ...))` 在大窗口上会超时
--      ⇒ ⇒ 内容指纹只在【几分钟窗口】内算；
--   ④ `pg_class` 没有 `relid` 列（★ 用 `c.oid`）；★ `pg_inherits` 的父子列是
--      `inhrelid` / `inhparent`（★ 不是 `relid` / `parent`）；
--   ⑤ `substring(x FROM '_([0-9]{8})$')` 匹配不到 `_legacy` ⇒ day_end 为 NULL
--      ⇒ ⇒ 【孤儿表永远进不了 DROP 型留存的候选集】；
--   ⑥ ★★ **孤儿表判定不能只看「不在 pg_inherits 里」**—— ★ 父表本身也
--      不在 `pg_inherits` 里（★ 它是 `relkind='p'` 的顶层对象）⇒ ⇒
--      ⇒ 必须【逐个列名点名】，★ 不能用「不在父表下」一刀切。
-- =====================================================================
SET statement_timeout = '600s';

\echo '=== 1/8: ★★★★★★ 族清单：★ 父表下 vs 孤儿（★ 逐个列名点名）★★ ★★★ 留存判据的候选集 ==='
SELECT c.relname AS relname,
       c.relkind AS relkind,
       CASE WHEN c.relkind = 'p' THEN 'parent-table'
            WHEN i.inhrelid IS NULL THEN 'ORPHAN-not-under-parent'
            ELSE 'under-parent' END AS attach_state,
       (substring(c.relname FROM '_([0-9]{8})$')::date + 1)::timestamp AT TIME ZONE 'Asia/Shanghai' AS day_end,
       CASE WHEN (substring(c.relname FROM '_([0-9]{8})$')::date + 1)::timestamp AT TIME ZONE 'Asia/Shanghai'
                 IS NULL THEN 'NO_DAY_END-never-dropped-by-partition-retention'
            WHEN (substring(c.relname FROM '_([0-9]{8})$')::date + 1)::timestamp AT TIME ZONE 'Asia/Shanghai'
                 <= (now() AT TIME ZONE 'Asia/Shanghai') - interval '7 days'
            THEN 'EXPIRED-should-be-dropped'
            ELSE 'kept' END AS retention_verdict,
       pg_total_relation_size(c.oid) AS total_bytes
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  LEFT JOIN pg_inherits i ON i.inhrelid = c.oid
 WHERE n.nspname = 'public'
   AND c.relname LIKE 'ursm_node_snapshot_min%'
   AND c.relkind IN ('r','p')
 ORDER BY pg_total_relation_size(c.oid) DESC;

\echo '=== 2/8: ★★★★★★ 存储账：★ 父表下 vs 孤儿 的体积占比 ==='
WITH rel AS (
  SELECT c.oid, c.relkind,
         pg_total_relation_size(c.oid) AS bytes,
         CASE WHEN c.relkind = 'p' THEN 'parent-table'
              WHEN i.inhrelid IS NULL THEN 'orphan'
              ELSE 'under-parent' END AS attach_state
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    LEFT JOIN pg_inherits i ON i.inhrelid = c.oid
   WHERE n.nspname = 'public'
     AND c.relname LIKE 'ursm_node_snapshot_min%'
     AND c.relkind IN ('r','p')
)
SELECT attach_state,
       count(*) AS tables,
       sum(bytes) AS total_bytes,
       pg_size_pretty(sum(bytes)) AS pretty
  FROM rel
 GROUP BY attach_state
 ORDER BY sum(bytes) DESC;

\echo '=== 3/8: ★★★★ 批间隔分布：★ 60s tick 的两实例相位错开 ⇒ 间隔应是【两个小间隔】而非 60 ==='
WITH b AS (
  SELECT snapshot_ts,
         lag(snapshot_ts) OVER (ORDER BY snapshot_ts) AS prev_ts
    FROM (SELECT DISTINCT snapshot_ts FROM public.ursm_node_snapshot_min_20261023
           WHERE snapshot_ts >= '2026-10-23 06:00:00+08'::timestamptz
             AND snapshot_ts <  '2026-10-23 06:20:00+08'::timestamptz) d
)
SELECT EXTRACT(EPOCH FROM (snapshot_ts - prev_ts))::int AS gap_seconds, count(*) AS n
  FROM b
 WHERE prev_ts IS NOT NULL
 GROUP BY 1
 ORDER BY 1;

\echo '=== 4/8: ★★★★★★ 内容级重复：★ 相邻两批逐字段指纹是否相同（★ 只在几分钟窗口内算）==='
WITH b AS (
  SELECT snapshot_ts,
         count(*) AS rows,
         sum(fail_streak) AS sfs,
         sum(available::int) AS avail,
         md5(string_agg(credential_id::text || '|' || raw_model_name || '|' || tenant_id || '|' ||
                        fail_streak::text || '|' || available::text || '|' || health_status, ',' ORDER BY credential_id::text, raw_model_name, tenant_id)) AS full_md5
    FROM public.ursm_node_snapshot_min_20261023
   WHERE snapshot_ts >= '2026-10-23 06:00:00+08'::timestamptz
     AND snapshot_ts <  '2026-10-23 06:04:00+08'::timestamptz
   GROUP BY snapshot_ts
)
SELECT a.snapshot_ts AS ts_a, b.snapshot_ts AS ts_b,
       a.rows AS rows_a, b.rows AS rows_b,
       a.sfs AS sfs_a, b.sfs AS sfs_b,
       a.avail AS avail_a, b.avail AS avail_b,
       CASE WHEN a.full_md5 = b.full_md5 THEN 'IDENTICAL' ELSE 'DIFFERENT' END AS content_cmp
  FROM b a
  JOIN b b ON b.snapshot_ts > a.snapshot_ts
 WHERE b.snapshot_ts - a.snapshot_ts < interval '40 seconds'
   AND b.snapshot_ts - a.snapshot_ts > interval '3 seconds'
 ORDER BY a.snapshot_ts, b.snapshot_ts;

\echo '=== 5/8: ★★★★★★ 每小时批次数：★ 60 tick × 2 实例 = 120 ⇒ ★ 唯一内容批 = 60 ==='
SELECT date_trunc('hour', snapshot_ts) AS hour_bucket,
       count(DISTINCT snapshot_ts) AS batches,
       count(*) AS rows
  FROM public.ursm_node_snapshot_min_20261023
 WHERE snapshot_ts >= '2026-10-23 00:00:00+08'::timestamptz
   AND snapshot_ts <  '2026-10-23 01:00:00+08'::timestamptz
 GROUP BY 1
 ORDER BY 1;

\echo '=== 6/8: ★★★★★★ legacy 的时间跨度：★ 它是否已被日分区完全取代 ==='
SELECT 'legacy' AS which,
       min(snapshot_ts) AS min_ts,
       max(snapshot_ts) AS max_ts,
       count(*) AS rows
  FROM public.ursm_node_snapshot_min_legacy;

\echo '=== 7/8: ★★★★ 每批的租户分布：★ 一批里 tenant_id 有几种取值 ==='
SELECT tenant_id,
       count(*) AS rows,
       count(DISTINCT raw_model_name) AS models
  FROM public.ursm_node_snapshot_min_20261023
 WHERE snapshot_ts = (SELECT min(snapshot_ts) FROM public.ursm_node_snapshot_min_20261023
                        WHERE snapshot_ts >= '2026-10-23 07:00:00+08'::timestamptz)
 GROUP BY tenant_id
 ORDER BY rows DESC;

\echo '=== 8/8: ★★★★★★ 留存配置的档位事实源：★ 生产 env 才是准（★ 库里没有）★★ ★★★ 用法见 §143.93 文档'
-- ★ ★★ 生产上 `URSM_SNAPSHOT_RETENTION_DAYS=7`（154 与 245 都有）⇒ cutoff = now - 7 天
-- ★ ★★ 而【孤儿表 legacy 的 day_end 为 NULL】⇒ ⇒ 永远不满足 `day_end <= cutoff`
-- ★ ★★ ⇒ ⇒ ⇒ 852 MB 永久滞留（★ 除非人工 DROP）
SELECT count(*) AS partitions_under_parent
  FROM pg_inherits i
 WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass;
-- =====================================================================
