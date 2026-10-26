-- ═══════════════════════════════════════════════════════════════════════════
-- verify_session_bodies_storage_shape.sql
--
-- 用途（§143.48，2026-10-21）：量化 `session_bodies` 族的**存储形态**，
--       为「storage.session_final_full_enabled」（final_full 合并 + 停写逐轮 outbound_body）
--       这个**已实现的热重载开关**重新评估收益。
--
-- ★★★ 为什么现在重新评估：
--   `settings/spec_storage.go` 里那个开关的 DescriptionLong 写着
--   「**生产实测（2026-10-02）：真实流量 98~99.9% 为单轮会话，存储收益近似为零**
--     ……故保持默认关闭，仅在**流量转向多轮形态**时按 storage-optimization-plan §4-12 重新评估」。
--   ⇒⇒ 那条判词给了一个**明确的重新评估触发条件**。
--   ⇒⇒ §143.47 实测 252 是 406,444 会话 / 1,063,889 轮 ⇒ **平均 2.62 轮/会话**
--   ⇒⇒ 「98~99.9% 单轮」这个前提**可能已经漂移** ⇒⇒ 本脚本量化当前真值。
--
-- ★ 机制（读 `domains/session/v2/bodies_writer.go`）：
--   · 关闭时：每轮一行 `kind='turn'`，**带** outbound_body
--   · 开启时：每轮一行 `kind='turn'`，**不带** outbound_body；
--             另 upsert 一行 `kind='final_full'`（turn_no=0，request_id='final_full:<session>'）
--   ⇒⇒ **收益 = 被停写的那些逐轮 outbound_body 的字节**
--   ⇒⇒ **代价 = 每会话多 1 行（+ 索引项 + 1 次冷启动探测）**
--             ＋ **前 k-1 轮的 outbound_body 永久丢失**（审计/取证回退）
--
-- ★★ 本脚本**只读**：全部语句为 SELECT，无 DDL/DML。
-- 用法：
--   252（经 154 跳板）：bash ~/.mavis-tools/lgwp-prod.sh <本文件> 900
--   211（LAN docker）：   ssh 211 'docker exec -i acc-pg17 psql -U acc_super \
--                                          -d llm_gateway -v ON_ERROR_STOP=1 -P pager=off -f -' < <本文件>
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '240s';
SET lock_timeout = '5s';
SET work_mem = '64MB';
SET jit = off;

\echo '### A. 开关当前值（★ 该开关是 platform scope + 热重载）'
SELECT key, value, scope, updated_at
  FROM public.settings_kv
 WHERE key IN ('storage.session_final_full_enabled',
               'storage.session_turn_bodies_enabled')
 ORDER BY key;

\echo '### B. session_bodies 的 kind 分布（★ 当前是否已有 final_full 行）'
SELECT COALESCE(kind, '<NULL>') AS kind,
       count(*) AS rows,
       count(*) FILTER (WHERE outbound_body IS NOT NULL) AS with_outbound,
       count(*) FILTER (WHERE request_delta IS NOT NULL) AS with_req_delta,
       count(*) FILTER (WHERE response_delta IS NOT NULL) AS with_resp_delta
  FROM public.session_bodies
 GROUP BY 1 ORDER BY 2 DESC;

\echo '### C. 每会话轮次分布（★ 「是否已转向多轮形态」的直接读数）'
WITH per_session AS (
  SELECT session_id, count(*) AS turns
    FROM public.session_turns
   GROUP BY session_id
)
SELECT turns, count(*) AS sessions,
       round(100.0 * count(*) / sum(count(*)) OVER (), 4) AS pct
  FROM per_session
 GROUP BY turns
 ORDER BY turns;

\echo '### C2. 轮次分布的三档汇总（★ 单轮 / 2 轮 / ≥3 轮）'
WITH per_session AS (
  SELECT session_id, count(*) AS turns
    FROM public.session_turns
   GROUP BY session_id
)
SELECT CASE WHEN turns = 1 THEN '1_single_turn'
            WHEN turns = 2 THEN '2_two_turns'
            WHEN turns >= 3 AND turns <= 5 THEN '3_3_to_5_turns'
            WHEN turns >= 6 AND turns <= 20 THEN '4_6_to_20_turns'
            ELSE '5_over_20_turns' END AS bucket,
       count(*) AS sessions,
       sum(turns) AS total_turns
  FROM per_session
 GROUP BY 1 ORDER BY 1;

\echo '### D. ★★ 中间轮 outbound_body 的字节（★ final_full 方案的可回收上限）'
-- ★ 口径：某会话的**最后一轮**的 outbound_body 会保留在 final_full 行里；
--   其余轮（turn_no < max_turn）的 outbound_body 会被**永久丢弃** ⇒ 那部分就是净收益。
WITH last_turn AS (
  SELECT session_id, max(turn_no) AS max_turn
    FROM public.session_turns
   GROUP BY session_id
)
SELECT count(*) AS bodies_rows,
       count(DISTINCT b.session_id) AS sessions,
       count(*) FILTER (WHERE b.turn_no < l.max_turn) AS non_last_rows,
       count(*) FILTER (WHERE b.turn_no = l.max_turn) AS last_rows,
       pg_size_pretty(sum(pg_column_size(b.outbound_body))) AS all_outbound_bytes,
       pg_size_pretty(sum(pg_column_size(b.outbound_body)) FILTER (WHERE b.turn_no < l.max_turn)) AS non_last_outbound_bytes,
       pg_size_pretty(sum(pg_column_size(b.outbound_body)) FILTER (WHERE b.turn_no = l.max_turn)) AS last_outbound_bytes
  FROM public.session_bodies b
  JOIN last_turn l ON l.session_id = b.session_id;

\echo '### E. ★★ 近 7 天流量（★ 「未来流量形态」的代理）'
-- ★★ 为什么单独量：final_full 只影响**开启之后**的新写入 ⇒ 历史存量不会因它而回收
--   ⇒⇒ 决定收益的是**当前流量形态**，不是历史累计形态。
WITH last_turn AS (
  SELECT session_id, max(turn_no) AS max_turn
    FROM public.session_turns
   GROUP BY session_id
), recent AS (
  SELECT b.*, l.max_turn
    FROM public.session_bodies b
    JOIN last_turn l ON l.session_id = b.session_id
   WHERE b.ts >= timestamptz '2026-10-14 00:00:00+08'
)
SELECT count(*) AS rows_7d,
       count(DISTINCT session_id) AS sessions_7d,
       count(*) FILTER (WHERE turn_no < max_turn) AS non_last_rows,
       pg_size_pretty(sum(pg_column_size(outbound_body))) AS all_outbound_bytes,
       pg_size_pretty(sum(pg_column_size(outbound_body)) FILTER (WHERE turn_no < max_turn)) AS non_last_outbound_bytes,
       pg_size_pretty(sum(pg_column_size(request_delta))) AS req_delta_bytes,
       pg_size_pretty(sum(pg_column_size(response_delta))) AS resp_delta_bytes
  FROM recent;

\echo '### F. ★ 三列 jsonb 的体积分布（★ 全量，看清 4.6 GB 花在哪）'
SELECT count(*) AS rows,
       pg_size_pretty(sum(pg_column_size(request_delta))) AS request_delta,
       pg_size_pretty(sum(pg_column_size(response_delta))) AS response_delta,
       pg_size_pretty(sum(pg_column_size(outbound_body))) AS outbound_body,
       pg_size_pretty(sum(pg_column_size(request_attachments))) AS request_attachments,
       pg_size_pretty(sum(pg_column_size(response_attachments))) AS response_attachments,
       pg_size_pretty(pg_total_relation_size('public.session_bodies')) AS whole_family
  FROM public.session_bodies;

\echo '### G. ★ 逐分区体积（★ 与 §143.46 的月度留存决策表对拍）'
SELECT c.relname AS part,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS size,
       c.reltuples::bigint AS approx_rows
  FROM pg_inherits i
  JOIN pg_class c ON c.oid = i.inhrelid
  JOIN pg_class p ON p.oid = i.inhparent
 WHERE p.relname = 'session_bodies'
 ORDER BY c.relname;
