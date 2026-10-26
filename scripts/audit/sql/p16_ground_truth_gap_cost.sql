-- §143.33 的 P16 量化查询（生产只读）
-- 用法：bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/sql/p16_ground_truth_gap_cost.sql 180
--
-- ★ 目的：量化 `stats_ground_truth_gap`（路由健康检查）的真实成本，
--   以裁决 §143.31 登记的 P16「CPU 侧竞争者」是否成立。
--   背景与结论见 runbook §143.33（订正横幅十二/十三/十四）。
--
-- ★ 只读：本文件只有 SELECT 与两个 SET（statement_timeout / jit），
--   不含任何写语句、DDL 或 TRUNCATE。
--
-- ⚠ 混库实例：必须按 dbid/datname 拆库 —— 下面的 JOIN pg_database 就是在做这件事
--   （本实例 dbid=16384）。

SET statement_timeout = '60s';

-- ── 1) P16 本身：calls / 总耗时 / 均时 / max / 物理读 / 每次块数 / 行数
--    ★ 指纹 = 「oldest」+「usage_facts」+「stats_usage_daily」三段同时命中
--      （单用其中一段会命中别的语句）。
SELECT 'gap' AS k,
       calls::text AS v1,
       round(total_exec_time::numeric, 1)::text AS v2,
       round(mean_exec_time::numeric, 1)::text AS v3,
       round(max_exec_time::numeric, 1)::text AS v4,
       shared_blks_read::text AS v5,
       round((shared_blks_read::numeric / NULLIF(calls, 0)), 0)::text AS v6,
       rows::text AS v7
FROM pg_stat_statements s
JOIN pg_database d ON d.oid = s.dbid
WHERE d.datname = current_database()
  AND s.query ILIKE '%oldest%'
  AND s.query ILIKE '%usage_facts%'
  AND s.query ILIKE '%stats_usage_daily%';

-- ── 2) 同窗口内按【总 DB 时间】排名的语句（§143.33 用它做「CPU/总时间线」并排）
--    ★ 注意 calls>=3 ⇒ 只在有足够样本的语句上比较。
SELECT 'rank' AS k, s.calls::text AS v1,
       round(s.mean_exec_time::numeric, 1)::text AS v2,
       s.shared_blks_read::text AS v3,
       round(s.total_exec_time::numeric, 1)::text AS v4,
       left(regexp_replace(s.query, '\s+', ' ', 'g'), 60) AS v5
FROM pg_stat_statements s
JOIN pg_database d ON d.oid = s.dbid
WHERE d.datname = current_database()
  AND s.calls >= 3
  AND s.query ILIKE '%WITH facts AS%'
ORDER BY s.total_exec_time DESC
LIMIT 5;

-- ── 3) 统计窗口 + 实例启动时间（决定「每场多少次」的分母）
--    ★ stats_reset 不是 NULL（本实例实测 2026-10-18 22:03:37）
--      ⇒ pgss 的窗口远短于 PG 的存活时间 ⇒ 两者不可混用。
--    ★ pg_postmaster_start_time 只用于「PG 起多久」，不是 pgss 的窗口。
SELECT 'info' AS k,
       coalesce(stats_reset::text, 'NULL') AS v1,
       (pg_postmaster_start_time()::text) AS v2,
       (now() - pg_postmaster_start_time())::interval::text AS v3
FROM pg_stat_statements_info;

-- ── 4) P16 每小时频次 + 占墙钟比例
--    ★ 每次跑一次本脚本就会多出一条 pgss 记录 ⇒ ★ **本脚本自身是读数的一部分**，
--      两次运行之间的差值是「又发生了 N 次」而不是「读数漂移」（见 §143.33 稳定性复读）。
SELECT 'gap_per_hour' AS k,
       s.calls::text AS v1,
       round((s.total_exec_time / 1000.0)::numeric, 1)::text AS v2,
       round((s.mean_exec_time)::numeric, 1)::text AS v3,
       round((extract(epoch from (now() - s2.stats_reset)) / 3600.0)::numeric, 1)::text AS v4_window_hours,
       round((100.0 * s.total_exec_time
              / NULLIF(extract(epoch from (now() - s2.stats_reset)) * 1000.0, 0))::numeric, 3)::text AS v5_pct_of_window
FROM pg_stat_statements s
JOIN pg_database d ON d.oid = s.dbid
CROSS JOIN pg_stat_statements_info s2
WHERE d.datname = current_database()
  AND s.query ILIKE '%oldest%'
  AND s.query ILIKE '%usage_facts%'
  AND s.query ILIKE '%stats_usage_daily%';
