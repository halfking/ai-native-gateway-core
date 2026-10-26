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

-- ═══════════════════════════════════════════════════════════════════════════
-- §143.59 新增（R9~R13）：把「没有自动回收路径的大对象」纳入登记册覆盖面，
-- 并提供**相对量锚点**（库级总量）⇒ 让门可以钉【比值/档位】而不是【绝对字节】。
--
-- ★ 为什么必须加 R11（库级总量）：
--   §143.58 实测：252 快照族字节从 10,193,199,104 B 变成 9,502,662,656 B
--   ——**1.4 小时内 −690 MB**（7 天窗口的日分区到期被删）⇒
--   「钉绝对字节数」这个钉法**天然会过期**⇒ §143.59 把门改成
--   **量级档位 + 相对量（族 ÷ 库总量）+ 读数日期戳 + 陈旧度上限**。
--   相对量（29.8% / 27.4%）在两侧都被自然增长/自然回收抵消 ⇒ 比绝对值耐用得多。
-- ═══════════════════════════════════════════════════════════════════════════

\echo '### R9 route_incident_events（留存 worker 实现完整但【零生产调用方】）'
SELECT count(*) AS rows_exact,
       min(created_at) AS min_created,
       max(created_at) AS max_created,
       count(*) FILTER (WHERE created_at < now() - interval '30 days') AS older_30d,
       count(*) FILTER (WHERE created_at < now() - interval '90 days') AS older_90d,
       pg_total_relation_size('public.route_incident_events'::regclass) AS total_bytes
  FROM public.route_incident_events;

\echo '### R9b route_incident_events 的 created_at 首列索引（worker 的硬前提）'
SELECT count(*) AS created_at_first_col_indexes
  FROM pg_index i
 WHERE i.indrelid = 'public.route_incident_events'::regclass
   AND (SELECT a.attname FROM pg_attribute a
         WHERE a.attrelid = i.indrelid AND a.attnum = i.indkey[0]) = 'created_at';

\echo '### R10 session_summaries（trimmer 在跑，但【目标集恒空】）'
SELECT count(*) AS rows_exact,
       count(*) FILTER (WHERE archived_at IS NOT NULL) AS archived_rows,
       count(*) FILTER (WHERE archived_at IS NULL)     AS active_rows,
       min(archived_at) FILTER (WHERE archived_at IS NOT NULL) AS min_archived,
       max(archived_at) FILTER (WHERE archived_at IS NOT NULL) AS max_archived,
       pg_total_relation_size('public.session_summaries'::regclass) AS total_bytes
  FROM public.session_summaries;

\echo '### R11 库级总量 + 快照族总量（相对量锚点：族 ÷ 库）'
SELECT pg_database_size(current_database()) AS db_total_bytes,
       (SELECT sum(pg_total_relation_size(c.oid))
          FROM pg_class c
          JOIN pg_namespace n ON n.oid = c.relnamespace
          JOIN pg_inherits i ON i.inhrelid = c.oid
         WHERE n.nspname = 'public'
           AND i.inhparent = 'public.ursm_node_snapshot_min'::regclass)
         AS snapshot_family_bytes;

\echo '### R12 机制存在性判据（settings_kv 里有没有那些键；刻意不读值）'
SELECT key, value_type
  FROM settings_kv
 WHERE key LIKE 'lifecycle.%ttl\_days'
    OR key = 'storage.session_final_full_enabled'
 ORDER BY key;

\echo '### R13 读数日期戳（★ 人工填表时**逐字复制**这一行到登记册表头）'
SELECT '-- reading-date: ' || to_char(now() AT TIME ZONE 'Asia/Shanghai', 'YYYY-MM-DD')
         AS header_line;
