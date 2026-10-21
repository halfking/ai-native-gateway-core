-- =====================================================================
-- scripts/audit/verify_outbound_accumulation_211.sql
-- §143.52 —— 「per-turn outbound_body 是不是累积快照」的 211 侧交叉验证
--
-- 口径（与 scripts/audit/verify_outbound_accumulation_252.sql 完全一致，
--   ★ 改口径后不得直接对拍两实例的绝对值）：
--   * 实例：211（docker 容器 acc-pg17，库 llm_gateway，用户 acc_super）
--   * 表：public.session_bodies_2026_10（直接点名分区）
--   * 窗口：2026-10-15 一整天（UTC）
--   * 字节：pg_column_size() = 解压后的逻辑大小
--
-- 只读：全文件仅 SELECT / SET。零 DDL、零 DML。
-- 用法（211 走 docker，不是跳板）：
--   ssh 211 'docker exec -i acc-pg17 psql -U acc_super -d llm_gateway \
--     -v ON_ERROR_STOP=1 -P pager=off -f -' < scripts/audit/verify_outbound_accumulation_211.sql
--   # 期望 5 段（D1~D5）、exit 0、零 ERROR
-- =====================================================================
SET statement_timeout='300s';
SET jit=off;

\echo '### D1. 211 上 session_bodies 的分区清单（★ 211 的分区与 252 不同：缺 2026_09）'
SELECT c.relname AS partition, pg_get_expr(c.relpartbound, c.oid) AS bound,
       c.reltuples::bigint AS est_rows
  FROM pg_class c
  JOIN pg_inherits i ON i.inhrelid = c.oid
  JOIN pg_class p ON p.oid = i.inhparent
 WHERE p.relname = 'session_bodies'
 ORDER BY 1;

\echo '### D2. ★ 211 字节按列归因 + 「停写 per-turn outbound_body」的可省字节（与 252 同口径）'
WITH r AS (
  SELECT session_id, tenant_id, turn_no,
         pg_column_size(outbound_body)       AS b_out,
         pg_column_size(request_delta)       AS b_req,
         pg_column_size(response_delta)      AS b_resp,
         pg_column_size(request_attachments) AS b_raq,
         pg_column_size(response_attachments) AS b_sa
    FROM public.session_bodies_2026_10 t
   WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00'
), agg AS (
  SELECT session_id, tenant_id,
         count(*) AS turns,
         coalesce(sum(b_out),0) AS s_out,
         coalesce(max(b_out),0) AS m_out,
         coalesce(sum(b_req+b_resp+b_raq+b_sa),0) AS s_other,
         count(*) FILTER (WHERE b_out > 0) AS rows_with_out
    FROM r GROUP BY session_id, tenant_id
)
SELECT CASE WHEN turns = 1 THEN '1 轮'
            WHEN turns <= 3 THEN '2-3 轮'
            WHEN turns <= 10 THEN '4-10 轮'
            WHEN turns <= 30 THEN '11-30 轮'
            WHEN turns <= 100 THEN '31-100 轮'
            ELSE '>100 轮' END AS turn_bucket,
       count(*) AS sessions,
       sum(turns) AS turns,
       sum(rows_with_out) AS rows_with_out,
       pg_size_pretty(sum(s_out)) AS outbound_bytes,
       pg_size_pretty(sum(m_out)) AS final_full_equivalent,
       pg_size_pretty(sum(s_out) - sum(m_out)) AS removable_bytes,
       pg_size_pretty(sum(s_other)) AS non_outbound_bytes,
       round(100.0 * (sum(s_out) - sum(m_out)) / NULLIF(sum(s_out)+sum(s_other),0), 2) AS pct_total_saved
  FROM agg
 GROUP BY 1
 ORDER BY min(turns);

\echo '### D3. 211 全局一天汇总（★ bytes_after_gate = 非 outbound + final_full 等价物）'
WITH r AS (
  SELECT session_id, tenant_id,
         pg_column_size(outbound_body)       AS b_out,
         pg_column_size(request_delta)       AS b_req,
         pg_column_size(response_delta)      AS b_resp,
         pg_column_size(request_attachments) AS b_raq,
         pg_column_size(response_attachments) AS b_sa
    FROM public.session_bodies_2026_10 t
   WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00'
), agg AS (
  SELECT session_id, tenant_id,
         count(*) AS turns,
         coalesce(sum(b_out),0) AS s_out,
         coalesce(max(b_out),0) AS m_out,
         coalesce(sum(b_req+b_resp+b_raq+b_sa),0) AS s_other
    FROM r GROUP BY session_id, tenant_id
)
SELECT count(*) AS sessions, sum(turns) AS turns,
       pg_size_pretty(sum(s_out))    AS total_outbound_bytes,
       pg_size_pretty(sum(m_out))    AS final_full_equivalent_bytes,
       pg_size_pretty(sum(s_other))  AS non_outbound_bytes,
       pg_size_pretty(sum(s_out)+sum(s_other)) AS bytes_today,
       pg_size_pretty(sum(s_other) + sum(m_out)) AS bytes_after_gate,
       round(100.0 * (sum(s_out)-sum(m_out)) / NULLIF(sum(s_out)+sum(s_other),0), 2) AS pct_total_saved
  FROM agg;

\echo '### D4. 211 kind 分布（★ final_full 行数 ⇒ 门当前开/关）'
SELECT kind, count(*) AS rows,
       count(*) FILTER (WHERE outbound_body IS NOT NULL AND outbound_body <> 'null'::jsonb) AS rows_with_out
  FROM public.session_bodies_2026_10 t
 WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00'
 GROUP BY 1 ORDER BY 1;

\echo '### D5. ★★ 211 单会话内 outbound 随 turn_no 演化（★ 压缩塌缩点 + 「同一份 50 kB 被逐行复制」）'
-- ★★ 本段是 252 没有的样本形态：
--   ① 某个 turn 之后 n_out 突然塌缩（压缩层把对话压回去）
--   ② 另一个会话里 n_out 长期不变 ⇒ 同一份约 50 kB 的对话被逐行复制 ⇒ 几十倍冗余
WITH big AS (
  SELECT session_id, tenant_id, count(*) AS turns
    FROM public.session_bodies_2026_10 t
   WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00'
   GROUP BY session_id, tenant_id
  HAVING count(*) > 10
   LIMIT 2
)
SELECT b.session_id, t.turn_no,
       CASE WHEN t.outbound_body IS NOT NULL AND t.outbound_body <> 'null'::jsonb
                 AND jsonb_typeof(t.outbound_body) = 'array'
            THEN jsonb_array_length(t.outbound_body) END AS n_out,
       pg_column_size(t.outbound_body) AS b_out,
       pg_column_size(t.request_delta) AS b_req
  FROM public.session_bodies_2026_10 t
  JOIN big b ON b.session_id = t.session_id AND b.tenant_id = t.tenant_id
 WHERE t.ts >= '2026-10-15 00:00:00+00' AND t.ts < '2026-10-16 00:00:00+00'
 ORDER BY b.session_id, t.turn_no
 LIMIT 60;
