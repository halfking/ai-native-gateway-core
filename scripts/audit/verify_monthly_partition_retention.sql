-- ═══════════════════════════════════════════════════════════════════════════
-- verify_monthly_partition_retention.sql
--
-- 用途：判定「月度分区族」的**留存机制是否真的在回收**，并量化
--       「按某个保留月数配置策略后能回收多少字节」。
--
-- 背景（§143.46，2026-10-21）：
--   月度分区族的 DROP 保留机制**早就建好了**（迁移 841）：
--     · 配置表 public.llm_gateway_partition_retention（每族一行 retain_months）
--     · 只读列举 public.llm_gateway_expired_month_partitions()
--     · 真正 DROP  public.llm_gateway_drop_expired_month_partitions()
--       （写审计表 public.llm_gateway_partition_drop_log）
--   且 bg/partition_manager.go **已经在调用** DROP 那个函数
--   （生产日志：「partition_manager: monthly partition retention sweep」）。
--
--   ★★ 但**配置表默认是空的** ⇒ 一个分区都不会被列为过期 ⇒ **机制空转**：
--     实测两实例配置表 0 行、审计表 0 行 ⇒ **从未删过任何月度分区**。
--
-- ★ 本脚本**只读**：全部语句为 SELECT / SET（会话参数），无 DDL/DML。
--
-- ★★★ **两处口径必须与 841 的实现逐字一致**（本轮各踩过一次）：
--  ① **过期判定**：841 的 cutoff = `cur_month - (N-1) months`，分区月 < cutoff 才过期
--     ⇔ `months_old >= N`。写成 `months_old < N` 会得到**反向**的数字。
--  ② **跨月差**：`timestamp - date` 得到的是**以天计**的 interval（"30 days"）
--     ⇒ `EXTRACT(month FROM ...)` **恒为 0** ⇒ 「几个月前」全算成 0
--     ⇒ ★ 在「算过期」的方向上极危险（把一切判成过期）。
--     ⇒ 正确做法：**月序号差**（year*12 + month 之差）。841 自己用的是
--        `date - interval 'N months'`（对的），本脚本改用月序号差以免再踩。
--
-- 用法：
--   252（经 154 跳板）：bash ~/.mavis-tools/lgwp-prod.sh <本文件> 300
--   211（LAN docker）： ssh 211 'docker exec -i acc-pg17 psql -U acc_super \
--                                  -d llm_gateway -v ON_ERROR_STOP=1 -f -' < <本文件>
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '240s';
SET lock_timeout = '5s';
SET work_mem = '32MB';
SET jit = off;

\echo '### A. 841 机制的三件套是否就位'
SELECT
  (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public' AND c.relname = 'llm_gateway_partition_retention') AS cfg_table,
  (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'public' AND p.proname = 'llm_gateway_expired_month_partitions') AS list_fn,
  (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'public' AND p.proname = 'llm_gateway_drop_expired_month_partitions') AS drop_fn,
  (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public' AND c.relname = 'llm_gateway_partition_drop_log') AS audit_table;

\echo '### B. 配置表内容（★ 空 ⇒ 机制空转）'
SELECT family, retain_months, enabled, note, updated_at
  FROM public.llm_gateway_partition_retention
 ORDER BY family;

\echo '### C. 配置表行数 与 审计表行数（两个 0 = 从未删过任何月度分区）'
SELECT (SELECT count(*) FROM public.llm_gateway_partition_retention) AS cfg_rows,
       (SELECT count(*) FROM public.llm_gateway_partition_drop_log) AS drop_log_rows;

\echo '### D. 当前会被列为过期的分区（配置为空 ⇒ 必然 0 行；配好策略后这里才会有行）'
SELECT family, parent_name, partition_name, partition_month, live_rows
  FROM public.llm_gateway_expired_month_partitions()
 ORDER BY family, partition_name;

\echo '### E. 月度分区族 × 体积（只认**直接子分区**且分区名形如 _YYYY_MM）'
SELECT parent.relname AS parent,
       count(*) AS n_month_parts,
       min(c.relname) AS oldest,
       max(c.relname) AS newest,
       sum(pg_total_relation_size(c.oid)) AS total_bytes,
       pg_size_pretty(sum(pg_total_relation_size(c.oid))) AS pretty
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  JOIN pg_class parent ON parent.oid = i.inhparent
  JOIN pg_namespace n ON n.oid = parent.relnamespace
 WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname ~ '_\d{4}_\d{2}$'
 GROUP BY parent.relname
 ORDER BY sum(pg_total_relation_size(c.oid)) DESC;

\echo '### F. 逐分区清单（父表 × 份月 × 字节 + 距今月数）'
SELECT parent.relname AS parent,
       c.relname AS partition,
       to_char(to_date(substring(c.relname FROM '_([0-9]{4}_\d{2})$'), 'YYYY_MM'), 'YYYY-MM') AS part_month,
       pg_total_relation_size(c.oid) AS bytes,
       ((EXTRACT(year FROM date_trunc('month', now()))::int * 12 + EXTRACT(month FROM date_trunc('month', now()))::int)
         - (EXTRACT(year FROM to_date(substring(c.relname FROM '_([0-9]{4}_\d{2})$'), 'YYYY_MM'))::int * 12
            + EXTRACT(month FROM to_date(substring(c.relname FROM '_([0-9]{4}_\d{2})$'), 'YYYY_MM'))::int)) AS months_old
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  JOIN pg_class parent ON parent.oid = i.inhparent
  JOIN pg_namespace n ON n.oid = parent.relnamespace
 WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname ~ '_\d{4}_\d{2}$'
 ORDER BY parent.relname, c.relname;

\echo '### G. 量化：保留 N 个月（含当月）⇒ 可回收字节（★ 决策表的输入）'
-- 语义与 841 一致：仅当 分区月 < 当月月初 - (N-1) 个月 才算过期。
WITH parts AS (
  SELECT parent.relname AS parent,
         to_date(substring(c.relname FROM '_([0-9]{4}_\d{2})$'), 'YYYY_MM') AS part_month,
         pg_total_relation_size(c.oid) AS bytes,
         ((EXTRACT(year FROM date_trunc('month', now()))::int * 12 + EXTRACT(month FROM date_trunc('month', now()))::int)
             - (EXTRACT(year FROM to_date(substring(c.relname FROM '_([0-9]{4}_\d{2})$'), 'YYYY_MM'))::int * 12
                + EXTRACT(month FROM to_date(substring(c.relname FROM '_([0-9]{4}_\d{2})$'), 'YYYY_MM'))::int)) AS months_old
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    JOIN pg_class parent ON parent.oid = i.inhparent
    JOIN pg_namespace n ON n.oid = parent.relnamespace
   WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname ~ '_\d{4}_\d{2}$'
)
-- ★ 过期语义**必须与 841 一致**：cutoff = 当月月初 - (N-1) 个月；
--   分区月 < cutoff 才过期 ⇔ months_old >= N。
--   ⚠ 写成 months_old < N 会得到**反向**的数字（把 N 越大算成回收越多）——
--     那正是本节踩过的第一个坑。
SELECT n_keep_months,
       count(*) FILTER (WHERE months_old <  n_keep_months) AS kept_parts,
       count(*) FILTER (WHERE months_old >= n_keep_months) AS droppable_parts,
       sum(bytes) FILTER (WHERE months_old >= n_keep_months) AS reclaimable_bytes,
       pg_size_pretty(coalesce(sum(bytes) FILTER (WHERE months_old >= n_keep_months), 0)) AS reclaimable_pretty
  FROM parts
 CROSS JOIN (VALUES (1), (2), (3), (6), (12)) AS k(n_keep_months)
 GROUP BY n_keep_months
 ORDER BY n_keep_months;

\echo '### H. 量化：按族 × 保留 N 个月（哪些族真正贡献可回收字节）'
WITH parts AS (
  SELECT parent.relname AS parent,
         to_date(substring(c.relname FROM '_([0-9]{4}_\d{2})$'), 'YYYY_MM') AS part_month,
         pg_total_relation_size(c.oid) AS bytes,
         ((EXTRACT(year FROM date_trunc('month', now()))::int * 12 + EXTRACT(month FROM date_trunc('month', now()))::int)
             - (EXTRACT(year FROM to_date(substring(c.relname FROM '_([0-9]{4}_\d{2})$'), 'YYYY_MM'))::int * 12
                + EXTRACT(month FROM to_date(substring(c.relname FROM '_([0-9]{4}_\d{2})$'), 'YYYY_MM'))::int)) AS months_old
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    JOIN pg_class parent ON parent.oid = i.inhparent
    JOIN pg_namespace n ON n.oid = parent.relnamespace
   WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname ~ '_\d{4}_\d{2}$'
)
SELECT parent,
       sum(bytes) FILTER (WHERE months_old >= 1) AS reclaim_keep1,
       sum(bytes) FILTER (WHERE months_old >= 2) AS reclaim_keep2,
       sum(bytes) FILTER (WHERE months_old >= 3) AS reclaim_keep3,
       sum(bytes) FILTER (WHERE months_old >= 6) AS reclaim_keep6
  FROM parts
 GROUP BY parent
 HAVING sum(bytes) FILTER (WHERE months_old >= 1) > 0
 ORDER BY reclaim_keep1 DESC;
