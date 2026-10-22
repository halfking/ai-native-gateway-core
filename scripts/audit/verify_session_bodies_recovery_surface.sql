-- §143.70 只读读数集合：会话正文的【恢复面】全量盘点
-- 用途：把「2026-09 的 159,831 个零正文会话能不能恢复」从空话变成可复算的数字。
-- 覆盖：① 会话族列名 ② 9 月逐日 turn ↔ bodies ③ turn 侧内容型候选列
--       ④ v1 族（session_turn_details / request_wal）形态 ⑤ WAL 归档 / PITR 可行性
--       ⑥ archive / 备份面盘点
--
-- ★ 只读：全部是 SELECT / 查元数据。
-- ★ 用法（252 共享库，经 154 跳板）：
--     bash ~/.mavis-tools/lgwp-prod.sh verify_session_bodies_recovery_surface.sql 1200
-- ★ 用法（211）：
--     ssh 211 'docker exec -i acc-pg17 psql -U acc_super -d llm_gateway -v ON_ERROR_STOP=1 -P pager=off -f -' \
--       < verify_session_bodies_recovery_surface.sql
--
-- ★ 预期结论（2026-10-22 实测，见 runbook §143.70）：
--   - 9 月 session_turns = 798,278 行 / 163,256 会话；
--   - 9 月 session_bodies 仅 2026-09-30 有 3,749 行（v2 正文路径的首批）；
--   - 9 月 turn 侧 request_delta / response_delta / search_text / trace_events 全 0；
--   - 9 月 request_preview 只覆盖 23.5%、response_preview 只覆盖 10.0%（且都是截断片段）；
--   - session_turn_details_2026_09（603 MB）无任何正文列；
--   - request_wal_2026_09（193 MB）无正文列；request_wal_bodies 仅 1 行；
--   - archive_mode=off ⇒ 252 上不可能 PITR 回放到 9 月。
--   ⇒ 唯一非零的恢复面是【网关侧降级兜底文件】（不在库里，需 SSH 到 154/245 打捞）。

\pset pager off
SET statement_timeout = 600000;

\echo '=== ① 会话族列名（连接键：request_id / session_id / delta）==='
SELECT table_name, ordinal_position, column_name, data_type
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name IN ('session_turns', 'session_bodies')
  AND (column_name ILIKE '%request_id%'
    OR column_name ILIKE '%session_id%'
    OR column_name ILIKE '%delta%'
    OR column_name ILIKE '%preview%'
    OR column_name ILIKE '%search_text%')
ORDER BY table_name, ordinal_position;

\echo '=== ② 9 月逐日：turn 行 vs bodies 行 ==='
SELECT t.partition_date,
       count(*)                                            AS turn_rows,
       count(DISTINCT t.session_id)                        AS turn_sessions,
       count(*) FILTER (WHERE t.request_delta IS NOT NULL) AS turn_with_request_delta,
       count(*) FILTER (WHERE t.response_delta IS NOT NULL) AS turn_with_response_delta
FROM public.session_turns t
WHERE t.partition_date >= DATE '2026-09-01'
  AND t.partition_date <= DATE '2026-09-30'
GROUP BY t.partition_date
ORDER BY t.partition_date;

SELECT b.partition_date,
       count(*)                     AS body_rows,
       count(DISTINCT b.session_id) AS body_sessions
FROM public.session_bodies b
WHERE b.partition_date >= DATE '2026-09-01'
  AND b.partition_date <= DATE '2026-09-30'
GROUP BY b.partition_date
ORDER BY b.partition_date;

\echo '=== ③ turn 侧内容型候选列的覆盖（preview 是截断片段，不是正文）==='
SELECT count(*)                                              AS rows_090,
       count(*) FILTER (WHERE request_preview IS NOT NULL)  AS n_request_preview,
       count(*) FILTER (WHERE response_preview IS NOT NULL) AS n_response_preview,
       count(*) FILTER (WHERE search_text IS NOT NULL)      AS n_search_text,
       count(*) FILTER (WHERE trace_events IS NOT NULL)     AS n_trace_events,
       avg(length(request_preview))                          AS avg_len_request_preview,
       max(length(request_preview))                          AS max_len_request_preview,
       avg(length(response_preview))                         AS avg_len_response_preview,
       max(length(response_preview))                         AS max_len_response_preview
FROM public.session_turns_2026_09;

\echo '=== ④ v1 族形态：session_turn_details / request_wal 是否有正文列 ==='
SELECT c.relname,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size,
       c.reltuples::bigint                          AS approx_rows
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND (c.relname LIKE 'session_turn_details%' OR c.relname LIKE 'request_wal%')
  AND c.relkind IN ('r', 'p')
ORDER BY c.relname;

SELECT 'request_wal_bodies' AS obj, count(*) AS rows FROM public.request_wal_bodies;

\echo '=== ⑤ WAL 归档 / PITR 可行性 ==='
SELECT name, setting, unit
FROM pg_settings
WHERE name IN ('archive_mode', 'archive_command', 'wal_level',
               'wal_keep_size', 'max_wal_size', 'restore_command');
SELECT archived_count, last_archived_wal, failed_count FROM pg_stat_archiver;

\echo '=== ⑥ archive / 备份面盘点（找可能的正文归档）==='
SELECT c.relname,
       c.relkind,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size,
       c.reltuples::bigint                            AS approx_rows
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r', 'p')
  AND (c.relname ILIKE '%archive%' OR c.relname ILIKE '%pre_cutover%'
    OR c.relname ILIKE '%request_log%' OR c.relname ILIKE '%bodies%')
ORDER BY pg_total_relation_size(c.oid) DESC
LIMIT 40;
