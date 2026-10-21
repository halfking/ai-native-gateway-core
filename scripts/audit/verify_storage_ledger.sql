-- =====================================================================
-- verify_storage_ledger.sql —— 存储存量台账（只读事实层）
--
-- 生成时间：2026-10-22（§143.58 轮）
-- 目的：给「存储与效率综合最优」这条线提供**一个可重跑的存量读数**，
--       覆盖 §143.43/§143.46 只覆盖「快照族 / usage_facts 族」之外的
--       **全库**对象（§143.58 实测：库级 30 GB，前两大族之外的
--       route_incident_events 1,156 MB + session_summaries 826 MB
--       在任何登记册里都**没有行**）。
--
-- ─────────────────────────────────────────────────────────────────────
-- ★★★ 分工纪律（本脚本的存在理由）
--
--   **事实层归脚本，判定层归登记册。**
--   本脚本只回答「有多少、什么时候的、什么形态、有没有索引」，
--   **不判定**「该不该回收」—— 那是
--   scripts/audit/retention_shape_findings.tsv +
--   admin/retention_shape_verdict.go 的职责。
--   （同 scripts/audit/verify_reclaim_candidates.sql 的定位：
--     「候选集【不】按『孤儿』筛 —— 按『大小』筛，把事实交给判定层」。）
--
-- ★ 三条量具纪律（沿用 §143.42 已确立的那三条）：
--   ① 判空用精确 count(*) / EXISTS，**禁止**用 pg_stat_user_tables.n_live_tup
--      （对「长期无写入 + 从未 analyze」的表恒为 0 ⇒ §143.40 误判的来源）
--   ② 判「分区/普通表/独立表」用 relkind + relispartition + pg_inherits **三者同时看**
--      （只查 pg_inherits 对普通表是恒真的空命题）
--   ③ 判「回收了多少字节」看磁盘（pg_total_relation_size），不看 catalog 汇总
--      ⇒ 且**分区父表的 pg_total_relation_size 恒为 0** ⇒ 族总量必须逐分区求和
--
-- ★ 本脚本【只读】：全部为 SELECT / WITH / SET（会话参数）。
--   不建对象、不写数据、不加锁到业务表。
--
-- 用法（252 共享库，经 154 跳板）：
--   cd <scratch> && bash ~/.mavis-tools/lgwp-prod.sh verify_storage_ledger.sql 240
-- 用法（211，LAN docker）：
--   ssh 211 'docker exec -i acc-pg17 psql -U acc_super -d llm_gateway \
--            -v ON_ERROR_STOP=1 -P pager=off -f -' < scripts/audit/verify_storage_ledger.sql
-- =====================================================================

\pset pager off
\timing off

SET statement_timeout = '120s';
SET lock_timeout = '5s';
SET work_mem = '32MB';

\echo '########## A. 库级基准（时区口径：841 的 cur_month 与 day_end 都随会话时区走）##########'
SELECT current_database() AS db,
       pg_size_pretty(pg_database_size(current_database())) AS db_total,
       (SELECT setting FROM pg_settings WHERE name = 'TimeZone') AS session_tz,
       now() AS now_at_tz,
       date_trunc('month', now())::date AS cur_month_841;

\echo '########## B. 全库 TOP 20 关系（含 TOAST）—— 按事实排，不按「看起来没用」筛 ##########'
SELECT c.relname,
       c.relkind,
       c.relispartition,
       (SELECT count(*) FROM pg_inherits i WHERE i.inhrelid = c.oid) AS n_parent,
       c.reltuples::bigint AS est_rows,
       pg_total_relation_size(c.oid)::bigint AS total_bytes,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_pretty,
       pg_relation_size(c.oid)::bigint AS heap_bytes,
       (pg_total_relation_size(c.oid) - pg_relation_size(c.oid))::bigint AS toast_bytes
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'm')
ORDER BY pg_total_relation_size(c.oid) DESC
LIMIT 20;

\echo '########## C. 分区族汇总（★ 族总量 = 逐分区求和；父表 pg_total_relation_size 恒为 0）##########'
SELECT parent.relname AS family,
       count(*) AS n_partitions,
       sum(pg_total_relation_size(child.oid))::bigint AS family_total_bytes,
       pg_size_pretty(sum(pg_total_relation_size(child.oid))) AS family_total_pretty,
       sum(child.reltuples::bigint) AS est_rows,
       min(to_date(substring(child.relname from '([0-9]{4}_[0-9]{2})$'), 'YYYY_MM')) AS first_month,
       max(to_date(substring(child.relname from '([0-9]{4}_[0-9]{2})$'), 'YYYY_MM')) AS last_month
FROM pg_inherits h
JOIN pg_class child ON child.oid = h.inhrelid
JOIN pg_class parent ON parent.oid = h.inhparent
JOIN pg_namespace pn ON pn.oid = parent.relnamespace
WHERE parent.relkind = 'p' AND pn.nspname = 'public'
GROUP BY parent.relname
ORDER BY sum(pg_total_relation_size(child.oid)) DESC;

\echo '########## D. 841 过期命中集（语义逐字照抄 sql/migrations/startup/841_monthly_partition_retention.sql）##########'
\echo '--- D1. cur_month / cutoff 的推导（841 用 date_trunc(''month'', now())，随会话时区）---'
WITH cfg(retain_months) AS (VALUES (1), (2), (3), (6), (12))
SELECT cfg.retain_months,
       (date_trunc('month', now())::date - ((cfg.retain_months - 1) || ' months')::interval)::date AS cutoff,
       count(*) AS hit_partitions,
       sum(pg_total_relation_size(child.oid))::bigint AS reclaim_bytes,
       pg_size_pretty(sum(pg_total_relation_size(child.oid))) AS reclaim_pretty
FROM cfg
JOIN pg_inherits h ON true
JOIN pg_class child ON child.oid = h.inhrelid
JOIN pg_class parent ON parent.oid = h.inhparent
JOIN pg_namespace pn ON pn.oid = parent.relnamespace
WHERE parent.relkind = 'p' AND pn.nspname = 'public'
  AND to_date(substring(child.relname from '([0-9]{4}_[0-9]{2})$'), 'YYYY_MM')
      < (date_trunc('month', now())::date - ((cfg.retain_months - 1) || ' months')::interval)::date
GROUP BY cfg.retain_months
ORDER BY cfg.retain_months;

\echo '--- D2. ★★ 命中集按族分组（N=1）—— 决定「先动哪个族」---'
SELECT parent.relname AS family,
       count(*) AS hit_partitions,
       sum(pg_total_relation_size(child.oid))::bigint AS reclaim_bytes,
       pg_size_pretty(sum(pg_total_relation_size(child.oid))) AS reclaim_pretty
FROM pg_inherits h
JOIN pg_class child ON child.oid = h.inhrelid
JOIN pg_class parent ON parent.oid = h.inhparent
JOIN pg_namespace pn ON pn.oid = parent.relnamespace
WHERE parent.relkind = 'p' AND pn.nspname = 'public'
  AND to_date(substring(child.relname from '([0-9]{4}_[0-9]{2})$'), 'YYYY_MM')
      < (date_trunc('month', now())::date)::date
GROUP BY parent.relname
ORDER BY sum(pg_total_relation_size(child.oid)) DESC;

\echo '########## E. ★★ 留存 worker「看不见」的对象（判定表与分区留存都按 pg_inherits 过滤）##########'
\echo '--- E1. 同前缀但【不是】分区父表子分区的独立表（relkind=r 且无父记录）---'
SELECT c.relname,
       c.relkind,
       c.relispartition,
       (SELECT count(*) FROM pg_inherits i WHERE i.inhrelid = c.oid) AS n_parent,
       c.reltuples::bigint AS est_rows,
       pg_total_relation_size(c.oid)::bigint AS total_bytes,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_pretty
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind = 'r'
  AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.oid)
  AND c.relname LIKE 'ursm_node_snapshot_min%'
ORDER BY pg_total_relation_size(c.oid) DESC;

\echo '########## F. ★★★ 三个「无自动回收路径」的大对象：年龄 + 机制前提 ##########'

\echo '--- F1. route_incident_events：行级 TTL 的【运行前提】= created_at 必须是某索引首列 ---'
SELECT EXISTS (
    SELECT 1
    FROM pg_index i
    JOIN pg_class ic ON ic.oid = i.indexrelid
    WHERE i.indrelid = 'public.route_incident_events'::regclass
      AND i.indisvalid AND i.indisready
      AND (SELECT array_agg(a.attname ORDER BY k.ord)
             FROM unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
             JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum)[1]
          = 'created_at'
) AS has_time_index,
count(*) AS rows_total,
min(created_at) AS min_created,
max(created_at) AS max_created,
count(*) FILTER (WHERE created_at < now() - interval '30 days') AS older_30d,
count(*) FILTER (WHERE created_at < now() - interval '90 days') AS older_90d,
pg_size_pretty(pg_total_relation_size('public.route_incident_events'::regclass)) AS total_pretty
FROM public.route_incident_events;

\echo '--- F2. session_summaries：trimmer 只删 archived_at IS NOT NULL ⇒ 活跃行永不被删 ---'
SELECT count(*) AS rows_total,
       count(*) FILTER (WHERE archived_at IS NULL) AS active_rows,
       count(*) FILTER (WHERE archived_at IS NOT NULL) AS archived_rows,
       min(archived_at) FILTER (WHERE archived_at IS NOT NULL) AS min_archived,
       max(archived_at) FILTER (WHERE archived_at IS NOT NULL) AS max_archived,
       pg_size_pretty(pg_total_relation_size('public.session_summaries'::regclass)) AS total_pretty
FROM public.session_summaries;

\echo '--- F3. ursm_node_snapshot_min_legacy：830 的回滚锚点（普通表，非分区）---'
SELECT (SELECT min(snapshot_ts) FROM public.ursm_node_snapshot_min_legacy) AS min_ts,
       (SELECT max(snapshot_ts) FROM public.ursm_node_snapshot_min_legacy) AS max_ts,
       (SELECT count(*) FROM public.ursm_node_snapshot_min_legacy) AS rows_total,
       (SELECT count(*) FROM public.ursm_node_snapshot_min_legacy
          WHERE snapshot_ts < now() - interval '7 days') AS rows_older_than_7d,
       pg_size_pretty(pg_total_relation_size('public.ursm_node_snapshot_min_legacy'::regclass)) AS total_pretty;

\echo '########## G. ★★ 会话族：E1 门开前后的逐轮正文写入形态（按 kind 拆）##########'
\echo '--- G1. 逐小时 × kind ---'
SELECT date_trunc('hour', ts) AS h,
       kind,
       count(*) AS rows,
       count(*) FILTER (WHERE pg_column_size(outbound_body) = 9) AS jsonb_null_rows,
       count(*) FILTER (WHERE pg_column_size(outbound_body) <> 9) AS real_body_rows,
       pg_size_pretty(sum(pg_column_size(outbound_body))) AS bytes
FROM public.session_bodies_unified
WHERE ts >= now() - interval '12 hours'
GROUP BY 1, 2
ORDER BY 1, 2;

\echo '--- G2. ★★ 每会话正文副本数（多轮会话才有副本化）---'
WITH per_sess AS (
  SELECT session_id,
         count(*) FILTER (WHERE kind = 'turn_delta') AS n_turn_rows,
         count(*) FILTER (WHERE kind = 'turn_delta' AND pg_column_size(outbound_body) <> 9) AS n_turn_real,
         count(*) FILTER (WHERE kind = 'final_full' AND pg_column_size(outbound_body) <> 9) AS n_ff_real,
         sum(pg_column_size(outbound_body)) FILTER (WHERE pg_column_size(outbound_body) <> 9) AS real_bytes
  FROM public.session_bodies_unified
  WHERE ts >= now() - interval '48 hours'
  GROUP BY session_id
  HAVING count(*) FILTER (WHERE kind = 'turn_delta') >= 2
)
SELECT count(*) AS multi_turn_sessions,
       sum(n_turn_rows) AS turn_rows,
       sum(n_turn_real) AS turn_rows_with_real_body,
       sum(n_ff_real)  AS snapshot_rows_with_real_body,
       round(avg(n_turn_real), 3) AS avg_turn_real_per_session,
       pg_size_pretty(sum(real_bytes)) AS real_body_bytes,
       pg_size_pretty(round(avg(real_bytes))) AS avg_bytes_per_session
FROM per_sess;
