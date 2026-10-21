-- ═══════════════════════════════════════════════════════════════════════════
-- refresh_retention_registry.sql
--
-- 用途：产出「快照族 / usage_facts 族留存形态登记册」的**当前生产事实**，
--       供 scripts/audit/retention_shape_findings.tsv 逐行填写。
--
-- ★ 为什么需要刷新（§143.45，2026-10-21）：§143.43 的登记册是**那个时点**的
--   生产快照；§143.44 在两实例上做了真实写入（211 分区化 + VACUUM FULL，
--   252 VACUUM FULL + DROP 空分区）⇒ 绝对字节数、relkind、行数**全部变了**。
--   ⇒ 登记册若不刷新，门钉的字节数就会指向一个已经不存在的世界。
--
-- ★ 本脚本**只读**：全部语句为 SELECT / SET（会话参数），无 DDL/DML。
--
-- 用法：
--   252（经 154 跳板）：bash ~/.mavis-tools/lgwp-prod.sh <本文件> 300
--   211（LAN docker）： ssh 211 'docker exec -i acc-pg17 psql -U acc_super \
--                                  -d llm_gateway -v ON_ERROR_STOP=1 -f -' < <本文件>
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '180s';
SET lock_timeout = '10s';
SET work_mem = '64MB';
SET jit = off;

\echo '### R1 快照族父表（relkind 决定走哪条留存路径）'
SELECT c.relkind,
       (SELECT count(*) FROM public.ursm_node_snapshot_min) AS rows_exact,
       pg_total_relation_size(c.oid) AS total_bytes,
       pg_relation_size(c.oid) AS heap_bytes,
       pg_indexes_size(c.oid) AS index_bytes
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public' AND c.relname = 'ursm_node_snapshot_min';

\echo '### R2 快照族 legacy（迁移回滚锚点）'
SELECT c.relkind,
       (SELECT count(*) FROM public.ursm_node_snapshot_min_legacy) AS rows_exact,
       pg_total_relation_size(c.oid) AS total_bytes
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public' AND c.relname = 'ursm_node_snapshot_min_legacy';

\echo '### R3 快照族日分区清单（day_end <= now()-7d 即过期）'
SELECT c.relname,
       (substring(c.relname FROM '_([0-9]{8})$')::date + 1)::timestamp
           AT TIME ZONE 'Asia/Shanghai' AS day_end_sh,
       pg_total_relation_size(c.oid) AS total_bytes
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
 WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass
 ORDER BY c.relname;

\echo '### R4 usage_facts 族（精确行数 + 体积）'
SELECT c.relkind,
       (SELECT count(*) FROM public.usage_facts) AS rows_exact,
       pg_total_relation_size(c.oid) AS total_bytes,
       pg_relation_size(c.oid) AS heap_bytes,
       pg_indexes_size(c.oid) AS index_bytes
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public' AND c.relname = 'usage_facts';

\echo '### R5 usage_facts 子对象（逐个精确行数：绝不用 reltuples 估算）'
SELECT c.relname,
       c.relkind,
       pg_relation_size(c.oid) AS heap_bytes,
       pg_indexes_size(c.oid) AS index_bytes,
       pg_total_relation_size(c.oid) AS total_bytes,
       (xpath('/row/c/text()',
              query_to_xml(format('SELECT count(*) AS c FROM %I.%I', n.nspname, c.relname),
                           false, true, '')))[1]::text::bigint AS rows_exact
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public'
   AND c.relname LIKE 'usage\_facts%'
   AND c.relkind IN ('r', 'p')
 ORDER BY c.relname;

\echo '### R6 留存登记的值无关判据（settings_kv 里有没有 TTL 键；刻意不读值）'
SELECT key, value_type, scope
  FROM settings_kv
 WHERE key LIKE 'lifecycle.%ttl\_days'
 ORDER BY key;

\echo '### R7 usage_facts 的 TTL 键是否存在（存在性判定，不读值）'
SELECT count(*) FILTER (WHERE key = 'lifecycle.usage_facts_ttl_days') AS usage_facts_ttl_key_present
  FROM settings_kv;

\echo '### R8 死元组（普通表形态的空间代价；分区父表恒为 0）'
SELECT s.relname, s.n_live_tup, s.n_dead_tup,
       pg_relation_size(s.relid) AS heap_bytes,
       pg_indexes_size(s.relid) AS index_bytes,
       pg_total_relation_size(s.relid) AS total_bytes
  FROM pg_stat_user_tables s
 WHERE s.relname IN ('ursm_node_snapshot_min', 'ursm_node_snapshot_min_legacy');
