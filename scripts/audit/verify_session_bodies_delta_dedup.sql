-- ═══════════════════════════════════════════════════════════════════════════
-- verify_session_bodies_delta_dedup.sql
--
-- 用途（§143.48，2026-10-21）：把「`request_delta` 与 `outbound_body` 是否同源双写」
--       这条存储疑点**量准**，并区分「真冗余」与「必要原料」。
--
-- ★★★ 先读仓内代码（权威事实源，不是猜）：
--   `domains/session/v2/outbound_builder.go` 的头注释写着
--     「1. 从多轮 request_delta + response_delta 拼接完整对话」
--   ⇒⇒ **`request_delta` 是「多轮对话重建」的原料，不是死列** ⇒ ⇒ **不能整体删**。
--   ⇒⇒ 真正的问题是：**哪些行的 `request_delta` 与同行的 `outbound_body` 逐字相同**
--   ⇒⇒ 那些行的 delta 就是冗余副本（读方可直接用 outbound_body 顶上）。
--
-- ★★★ 本轮已量到的前提（252，overlap 段 A1~A3）：
--   · `request_delta`：SQL NULL 0 / **jsonb null 28,735** / 是数组 241,158
--   · `outbound_body` ：SQL NULL 0 / **jsonb null 190,000** / 是数组 79,893
--   · **逐字相同（`request_delta = outbound_body`）= 77,005 行**
--   · 抽样（A5）里 identical 行**全部是 turn_no = 1**
--
-- ★★ 本脚本**只读**：全部语句为 SELECT，无 DDL/DML。
-- 用法：bash ~/.mavis-tools/lgwp-prod.sh <本文件> 900
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '240s';
SET lock_timeout = '5s';
SET work_mem = '64MB';
SET jit = off;

\echo '### D1. ★★★ identical 行的规模与体积（★ 「可去重副本」到底有多大）'
SELECT count(*) AS rows,
       count(*) FILTER (WHERE request_delta = outbound_body) AS identical_rows,
       pg_size_pretty(sum(pg_column_size(request_delta)) FILTER (WHERE request_delta = outbound_body)) AS identical_req_bytes,
       pg_size_pretty(sum(pg_column_size(request_delta))) AS all_req_bytes,
       count(*) FILTER (WHERE request_delta IS DISTINCT FROM outbound_body) AS differing_rows,
       pg_size_pretty(sum(pg_column_size(request_delta)) FILTER (WHERE request_delta IS DISTINCT FROM outbound_body)) AS differing_req_bytes
  FROM public.session_bodies
 WHERE kind = 'turn_delta';

\echo '### D2. ★★★ identical 行是不是「单轮会话的第一轮」（★ 决定它是不是真冗余）'
WITH lt AS MATERIALIZED (
  SELECT session_id, max(turn_no) AS max_turn, count(*) AS session_turns
    FROM public.session_turns
   GROUP BY session_id
)
SELECT lt.session_turns,
       count(*) AS identical_rows,
       count(*) FILTER (WHERE b.turn_no = 1) AS with_turn_no_1,
       count(*) FILTER (WHERE b.turn_no = lt.max_turn) AS is_last_turn,
       pg_size_pretty(sum(pg_column_size(b.request_delta))) AS req_bytes
  FROM public.session_bodies b
  JOIN lt ON lt.session_id = b.session_id
 WHERE b.kind = 'turn_delta'
   AND b.request_delta = b.outbound_body
 GROUP BY 1
 ORDER BY 1
 LIMIT 15;

\echo '### D3. ★★★ 「单轮会话」整体可去重的量（★ 若 delta ≡ outbound，删 delta 读方可顶上）'
WITH lt AS MATERIALIZED (
  SELECT session_id, count(*) AS session_turns
    FROM public.session_turns
   GROUP BY session_id
)
SELECT count(*) AS single_turn_rows,
       pg_size_pretty(sum(pg_column_size(b.request_delta))) AS single_turn_req_bytes,
       pg_size_pretty(sum(pg_column_size(b.outbound_body))) AS single_turn_outbound_bytes,
       count(*) FILTER (WHERE b.request_delta = b.outbound_body) AS identical_rows,
       count(*) FILTER (WHERE b.outbound_body = 'null'::jsonb) AS outbound_is_json_null,
       count(*) FILTER (WHERE b.request_delta = 'null'::jsonb) AS req_is_json_null
  FROM public.session_bodies b
  JOIN lt ON lt.session_id = b.session_id
 WHERE b.kind = 'turn_delta'
   AND lt.session_turns = 1;

\echo '### D4. ★ 多轮会话里，delta 与 outbound 的关系（★ 证明它们「不是冗余」）'
WITH lt AS MATERIALIZED (
  SELECT session_id, count(*) AS session_turns
    FROM public.session_turns
   GROUP BY session_id
)
SELECT lt.session_turns,
       count(*) AS rows,
       count(*) FILTER (WHERE b.request_delta = b.outbound_body) AS identical_rows,
       count(*) FILTER (WHERE b.request_delta @> b.outbound_body) AS delta_contains_outbound,
       count(*) FILTER (WHERE b.outbound_body @> b.request_delta) AS outbound_contains_delta,
       avg(CASE WHEN jsonb_typeof(b.request_delta)='array' THEN jsonb_array_length(b.request_delta) END)::numeric(12,2) AS avg_req_msgs,
       avg(CASE WHEN jsonb_typeof(b.outbound_body)='array' THEN jsonb_array_length(b.outbound_body) END)::numeric(12,2) AS avg_out_msgs
  FROM public.session_bodies b
  JOIN lt ON lt.session_id = b.session_id
 WHERE b.kind = 'turn_delta'
   AND lt.session_turns > 1
 GROUP BY 1
 ORDER BY 1
 LIMIT 15;
