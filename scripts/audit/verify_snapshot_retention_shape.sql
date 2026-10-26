-- ═══════════════════════════════════════════════════════════════════════════
-- verify_snapshot_retention_shape.sql
--
-- 用途：判定「快照族（ursm_node_snapshot_min）与 usage_facts 族」的**留存形态**
--       与**可回收空间**，在任意实例上**只读**跑一遍即可复现 §143.43 的结论。
--
-- 背景（§143.43，2026-10-21）：
--   快照族的留存是**双模**的（domains/ursm/v2/persist/retention_partition.go）：
--       表是普通 heap（迁移 830 未执行） ⇒ 走行级 DELETE
--       表是分区父表（relkind='p'）      ⇒ 走逐分区 DROP
--   ⇒ **形态由数据库自己回答**，不读任何配置开关。
--   本脚本判的就是这个「形态」，并给出两种形态各自的空间代价。
--
-- ★ 本脚本**只读**：全部语句为 SELECT / WITH / SET（会话参数）。
--   刻意**不含**任何 DDL/DML，也不含 VACUUM/ANALYZE ⇒ 可安全指向生产。
--
-- 用法：
--   252（经 154 跳板）：bash ~/.mavis-tools/lgwp-prod.sh <本文件> 200
--   211（LAN docker）： ssh 211 'docker exec -i acc-pg17 psql -U acc_super \
--                                  -d llm_gateway -v ON_ERROR_STOP=1 -f -' < <本文件>
--   本地一次性库：      docker exec -i llm-gateway-pg psql -U llm_gateway -d <db> -f - < <本文件>
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '90s';
SET lock_timeout = '10s';
SET work_mem = '64MB';
SET jit = off;

\echo
\echo '########## A. 快照族形态（relkind 决定走哪条留存路径）##########'
SELECT c.relname,
       c.relkind,
       CASE c.relkind WHEN 'p' THEN 'partitioned ⇒ DROP 型留存（空间真归还）'
                      ELSE 'plain heap ⇒ 行级 DELETE（空间不归还）' END AS retention_path,
       c.reltuples::bigint AS est_rows,
       pg_total_relation_size(c.oid) AS total_bytes
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public'
   AND c.relkind IN ('r', 'p')
   AND c.relname LIKE 'ursm\_node\_snapshot\_min%'
 ORDER BY c.relname;

\echo
\echo '########## B. 分区清单（按日分区的 day_end 决定是否过期）##########'
SELECT c.relname,
       substring(c.relname FROM '_([0-9]{8})$') AS part_day,
       (substring(c.relname FROM '_([0-9]{8})$')::date + 1)::timestamp
           AT TIME ZONE 'Asia/Shanghai' AS day_end_sh,
       (substring(c.relname FROM '_([0-9]{8})$')::date + 1)::timestamp
           AT TIME ZONE 'Asia/Shanghai' <= now() - interval '7 days' AS expired_at_7d,
       c.reltuples::bigint AS est_rows,
       pg_total_relation_size(c.oid) AS total_bytes
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
 WHERE i.inhparent = to_regclass('public.ursm_node_snapshot_min')
 ORDER BY c.relname;

\echo
\echo '########## C. 快照族时间跨度与超期行（行级留存的判据）##########'
SELECT min(snapshot_ts) AS ts_min,
       max(snapshot_ts) AS ts_max,
       now() AT TIME ZONE 'Asia/Shanghai' AS now_sh,
       count(*) FILTER (WHERE snapshot_ts < now() - interval '7 days') AS rows_older_7d,
       count(*) AS total_rows
  FROM public.ursm_node_snapshot_min;

\echo
\echo '########## D. 死元组与可回收估算（DELETE 形态的空间代价）##########'
SELECT s.relname,
       s.n_live_tup,
       s.n_dead_tup,
       CASE WHEN s.n_live_tup > 0
            THEN round(100.0 * s.n_dead_tup / (s.n_live_tup + s.n_dead_tup), 2)
            ELSE NULL END AS dead_pct,
       pg_relation_size(s.relid) AS heap_bytes,
       pg_indexes_size(s.relid) AS index_bytes,
       pg_total_relation_size(s.relid) AS total_bytes,
       s.last_autovacuum
  FROM pg_stat_user_tables s
 WHERE s.relname = 'ursm_node_snapshot_min';

\echo
\echo '########## E. usage_facts 族：分区清单 + DEFAULT 膨胀 ##########'
SELECT c.relname,
       c.relkind,
       c.reltuples::bigint AS est_rows,
       pg_relation_size(c.oid) AS heap_bytes,
       pg_indexes_size(c.oid) AS index_bytes,
       pg_total_relation_size(c.oid) AS total_bytes
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public'
   AND c.relkind IN ('r', 'p')
   AND c.relname LIKE 'usage\_facts%'
 ORDER BY c.relname;

\echo
\echo '########## F. usage_facts 数据跨度与日均增长（无界增长的量化）##########'
SELECT min(occurred_at) AS d_min,
       max(occurred_at) AS d_max,
       count(*) AS rows,
       pg_size_pretty(pg_total_relation_size(to_regclass('public.usage_facts'))) AS parent_size
  FROM public.usage_facts;

\echo
\echo '########## G. 留存登记的**值无关**结构性判据（settings_kv 里有没有 TTL 键）##########'
-- ★ 刻意**不读值**：只判「键在不在」。值可能已被改过、清掉或从未设置；
--   「有没有这个键」是可以在不泄露任何配置内容的前提下复核的事实。
SELECT key,
       value_type,
       scope
  FROM settings_kv
 WHERE key LIKE 'lifecycle.%ttl\_days'
 ORDER BY key;

\echo
\echo '########## H. 结论表（人工判读用；脚本只给事实，不给结论）##########'
-- 事实 → 结论的映射见 docs/audit/2026-10-21-R80-§143.43-快照族留存形态复核.md
\echo '判读要点：'
\echo '  A 表 relkind='p'  ⇒ DROP 型留存 ⇒ 占用 O(保留天数)，空间真归还'
\echo '  A 表 relkind='r'  ⇒ 行级 DELETE ⇒ 行数被窗口限定，但空间不归还（看 D 的 dead_pct）'
\echo '  B 分区清单里 expired_at_7d=true 的分区仍存在 ⇒ DROP 路径没跑'
\echo '  F/G usage_facts 无 TTL 键 ⇒ 该族没有任何留存 ⇒ 唯一真正的无界增长族'
