-- =====================================================================
-- scripts/audit/verify_outbound_accumulation_252.sql
-- §143.52 —— 「per-turn outbound_body 是不是累积快照」的生产只读量化
--
-- 口径（§143.51 / §143.52 通用，不得改口径后直接对拍旧数）：
--   * 实例：252（172.16.2.210:5432/llm_gateway，经 154 跳板）
--   * 表：public.session_bodies_2026_10（直接点名分区；⚠ 本库
--     `FROM tbl PARTITION (p)` 会返回整表，禁止使用）
--   * 窗口：2026-10-15 一整天（UTC）：ts >= '2026-10-15 00:00:00+00'
--          AND ts <  '2026-10-16 00:00:00+00'
--     ⇒ ★ 为什么用一天而不是全表：本库 session_bodies 分区全表
--       pg_column_size 扫描 60 s 超时（§143.50 实测）。
--   * 字节口径：pg_column_size() 返回【解压后的逻辑大小】
--     （生产实测 sum(pg_column_size)/heap_size = 10.29 ⇒ 逻辑大小
--      是占盘的 10 倍）⇒ 本脚本的字节数是【逻辑字节】，不是落盘字节。
--   * 轮次桶：按 (session_id, tenant_id) 的行数分桶。
--
-- 只读：全文件仅 SELECT / SET。零 DDL、零 DML。
-- 用法：
--   bash ~/.mavis-tools/lgwp-prod.sh scripts/audit/verify_outbound_accumulation_252.sql 420 > out.log 2>&1
--   grep -c '^### ' out.log          # 期望 8 段（W1~W8）
--   grep -niE 'error|fatal|42P' out.log # 期望 0 行
-- =====================================================================
SET statement_timeout='300s';
SET jit=off;

\echo '### W1. 会话/轮数分布 + 行级 outbound 覆盖率（★ 按行数算覆盖率，不按会话内比例）'
-- ★ 口径修正（§143.52 自纠）：⚠ 早期版本的 `avg_frac_rows_with_out`
--   写成了 avg(rows_with_out) * 1.0 / count(*) ⇒ 除的是【桶内会话数】
--   而不是【会话内轮数】⇒ 该列是无意义的公式 bug，整列作废。
--   正确读数 = rows_with_out（行数）/ turns（轮数）。
WITH r AS (
  SELECT session_id, tenant_id, turn_no,
         pg_column_size(t) AS row_bytes,
         (outbound_body IS NOT NULL AND outbound_body <> 'null'::jsonb) AS has_out
    FROM public.session_bodies_2026_10 t
   WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00'
), agg AS (
  SELECT session_id, tenant_id,
         count(*) AS turns,
         count(*) FILTER (WHERE has_out) AS rows_with_out,
         sum(row_bytes) AS total_bytes,
         max(row_bytes) AS max_bytes
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
       round(100.0 * sum(rows_with_out) / NULLIF(sum(turns),0), 2) AS pct_rows_with_out,
       pg_size_pretty(sum(total_bytes)) AS bytes_total,
       pg_size_pretty(sum(max_bytes))  AS bytes_final_only,
       round(sum(total_bytes)::numeric / NULLIF(sum(max_bytes),0), 2) AS amplification_vs_final_only
  FROM agg
 GROUP BY 1
 ORDER BY min(turns);

\echo '### W2. 全局一天：★ 放大倍数 + 「停写 per-turn outbound_body」后的字节（★ 公式已修正）'
-- ★ 自纠（§143.52）：早期版本的 bytes_after_gate 写成
--   (sum(s_out) - sum(m_out) + sum(s_other))
--   ⇒ 把 final_full 等价物【减掉】了 ⇒ 与同段 pct_total_saved 自相矛盾
--   （列显示 −7.6%、百分比显示 −44%）。
--   ★ 正确值 = sum(s_other) + sum(m_out)：非 outbound 部分【加上】
--     final_full 等价物（每会话一行最后完整快照）。
WITH r AS (
  SELECT session_id, tenant_id,
         pg_column_size(outbound_body) AS b_out,
         pg_column_size(request_delta) AS b_req,
         pg_column_size(response_delta) AS b_resp,
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
SELECT count(*) AS sessions,
       sum(turns) AS turns,
       pg_size_pretty(sum(s_out))  AS total_outbound_bytes,
       pg_size_pretty(sum(m_out))  AS final_full_equivalent_bytes,
       pg_size_pretty(sum(s_other)) AS non_outbound_bytes,
       pg_size_pretty(sum(s_out) + sum(s_other)) AS bytes_today,
       pg_size_pretty(sum(s_other) + sum(m_out)) AS bytes_after_gate,
       pg_size_pretty(sum(s_out) - sum(m_out)) AS bytes_removable,
       round(100.0 * (sum(s_out)-sum(m_out)) / NULLIF(sum(s_out)+sum(s_other),0), 2) AS pct_total_saved,
       round(100.0 * (sum(s_out)-sum(m_out)) / NULLIF(sum(s_out),0), 2) AS pct_of_outbound_saved
  FROM agg;

\echo '### W3. 字节占比：★ 「多轮会话」占多少字节（会话数占比 vs 字节占比）'
WITH r AS (
  SELECT session_id, tenant_id, pg_column_size(t) AS row_bytes
    FROM public.session_bodies_2026_10 t
   WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00'
), agg AS (
  SELECT session_id, tenant_id, count(*) AS turns, sum(row_bytes) AS total_bytes
    FROM r GROUP BY session_id, tenant_id
)
SELECT CASE WHEN turns = 1 THEN '1 轮'
            WHEN turns <= 3 THEN '2-3 轮'
            WHEN turns <= 10 THEN '4-10 轮'
            WHEN turns <= 30 THEN '11-30 轮'
            ELSE '>30 轮' END AS turn_bucket,
       count(*) AS sessions,
       sum(turns) AS turns,
       pg_size_pretty(sum(total_bytes)) AS bytes,
       round(100.0 * count(*) / NULLIF(sum(count(*)) OVER (),0), 3) AS pct_of_sessions,
       round(100.0 * sum(total_bytes) / NULLIF((SELECT sum(total_bytes) FROM agg),0), 2) AS pct_of_bytes
  FROM agg
 GROUP BY 1
 ORDER BY min(turns);

\echo '### W4. 字节按列归因：★ outbound_body / request_delta / response_delta / attachments'
WITH r AS (
  SELECT session_id, tenant_id, turn_no,
         pg_column_size(outbound_body) AS b_out,
         pg_column_size(request_delta) AS b_req,
         pg_column_size(response_delta) AS b_resp,
         pg_column_size(request_attachments) AS b_raq,
         pg_column_size(response_attachments) AS b_sa
    FROM public.session_bodies_2026_10 t
   WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00'
), agg AS (
  SELECT session_id, tenant_id, count(*) AS turns,
         coalesce(sum(b_out),0)  AS s_out,
         coalesce(sum(b_req),0)  AS s_req,
         coalesce(sum(b_resp),0) AS s_resp,
         coalesce(sum(b_raq),0)  AS s_raq,
         coalesce(sum(b_sa),0)   AS s_sa,
         coalesce(sum(b_out+b_req+b_resp+b_raq+b_sa),0) AS s_tot
    FROM r GROUP BY session_id, tenant_id
)
SELECT CASE WHEN turns = 1 THEN '1 轮'
            WHEN turns <= 3 THEN '2-3 轮'
            WHEN turns <= 10 THEN '4-10 轮'
            WHEN turns <= 30 THEN '11-30 轮'
            WHEN turns <= 100 THEN '31-100 轮'
            ELSE '>100 轮' END AS turn_bucket,
       count(*) AS sessions, sum(turns) AS turns,
       pg_size_pretty(sum(s_out))  AS bytes_outbound,
       pg_size_pretty(sum(s_req))  AS bytes_request_delta,
       pg_size_pretty(sum(s_resp)) AS bytes_response_delta,
       pg_size_pretty(sum(s_raq))  AS bytes_req_attach,
       pg_size_pretty(sum(s_sa))   AS bytes_resp_attach,
       pg_size_pretty(sum(s_tot))  AS bytes_total,
       round(100.0 * sum(s_out)  / NULLIF(sum(s_tot),0), 2) AS pct_outbound,
       round(100.0 * sum(s_req)  / NULLIF(sum(s_tot),0), 2) AS pct_request_delta,
       round(100.0 * sum(s_resp) / NULLIF(sum(s_tot),0), 2) AS pct_response_delta
  FROM agg
 GROUP BY 1
 ORDER BY min(turns);

\echo '### W5. ★ final_full 等价物：★ 停写 per-turn outbound_body 后可省多少（望远镜求和口径）'
-- ★ 机制（708 S1b / storage plan v2 D2）：final_full 门打开 ⇒
--   per-turn 行 outbound_body=nil；每会话另存 1 行 final_full（最后完整 outbound）
--   ⇒ 存储从「Σ_i C_i」变成「Σ_i delta_i + C_N」
--   ⇒ ★ 望远镜求和：Σ delta_i = C_N ⇒ 「Σ_i C_i − C_N」= 可省字节
WITH r AS (
  SELECT session_id, tenant_id, turn_no,
         pg_column_size(outbound_body) AS b_out,
         CASE WHEN outbound_body IS NOT NULL AND outbound_body <> 'null'::jsonb
                   AND jsonb_typeof(outbound_body) = 'array'
              THEN jsonb_array_length(outbound_body) END AS n_out
    FROM public.session_bodies_2026_10 t
   WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00'
), agg AS (
  SELECT session_id, tenant_id, count(*) AS turns,
         coalesce(sum(b_out),0) AS s_out,
         coalesce(max(b_out),0) AS m_out,
         max(n_out) AS m_n,
         count(*) FILTER (WHERE n_out IS NOT NULL) AS rows_with_out
    FROM r GROUP BY session_id, tenant_id
)
SELECT CASE WHEN turns = 1 THEN '1 轮'
            WHEN turns <= 3 THEN '2-3 轮'
            WHEN turns <= 10 THEN '4-10 轮'
            WHEN turns <= 30 THEN '11-30 轮'
            WHEN turns <= 100 THEN '31-100 轮'
            ELSE '>100 轮' END AS turn_bucket,
       count(*) AS sessions, sum(turns) AS turns, sum(rows_with_out) AS rows_with_out,
       pg_size_pretty(sum(s_out)) AS sum_outbound_bytes,
       pg_size_pretty(sum(m_out)) AS sum_final_full_equivalent,
       pg_size_pretty(sum(s_out) - sum(m_out)) AS bytes_removable,
       round(100.0 * (sum(s_out) - sum(m_out)) / NULLIF(sum(s_out),0), 2) AS pct_of_outbound_removable,
       round(avg(m_n), 1) AS avg_max_out_msgs,
       round(avg(rows_with_out), 1) AS avg_rows_with_out
  FROM agg
 GROUP BY 1
 ORDER BY min(turns);

\echo '### W6. ★★ 冗余度：★ per-turn outbound_body 与【同一行自己的 request_delta】是否近似重复'
-- ★★ 头条证据（§143.52）：多轮会话里同一行的 outbound_body 与 request_delta
--   字节近似相等 ⇒ ⇒ per-turn outbound_body 在该行内就是 request_delta 的副本
--   ⇒ ⇒ 且跨行重复（同一份压缩后对话被逐行复制）⇒ ⇒ 存储冗余可量化。
WITH r AS (
  SELECT session_id, tenant_id, turn_no,
         pg_column_size(outbound_body) AS b_out,
         pg_column_size(request_delta) AS b_req
    FROM public.session_bodies_2026_10 t
   WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00'
), s AS (
  SELECT session_id, tenant_id, count(*) AS turns
    FROM r GROUP BY session_id, tenant_id
), j AS (
  SELECT r.*, s.turns FROM r JOIN s USING (session_id, tenant_id)
   WHERE r.b_out > 0
)
SELECT CASE WHEN turns = 1 THEN '1 轮'
            WHEN turns <= 3 THEN '2-3 轮'
            WHEN turns <= 10 THEN '4-10 轮'
            WHEN turns <= 30 THEN '11-30 轮'
            WHEN turns <= 100 THEN '31-100 轮'
            ELSE '>100 轮' END AS turn_bucket,
       count(*) AS rows_with_out,
       count(*) FILTER (WHERE b_req > 0
                          AND abs(b_out - b_req) <= 0.10 * GREATEST(b_out, b_req)) AS rows_outbound_eq_request_delta,
       round(100.0 * count(*) FILTER (WHERE b_req > 0
                          AND abs(b_out - b_req) <= 0.10 * GREATEST(b_out, b_req))
             / NULLIF(count(*),0), 2) AS pct_outbound_is_dup_of_request_delta,
       pg_size_pretty(sum(b_out)) AS sum_outbound,
       pg_size_pretty(sum(b_req)) AS sum_request_delta,
       round(sum(b_out)::numeric / NULLIF(sum(b_req),0), 3) AS out_over_req_ratio
  FROM j
 GROUP BY 1
 ORDER BY min(turns);

\echo '### W7. kind 分布（★ final_full 行数 ⇒ 门当前开/关）'
SELECT kind,
       count(*) AS rows,
       count(*) FILTER (WHERE outbound_body IS NOT NULL AND outbound_body <> 'null'::jsonb) AS rows_with_out,
       pg_size_pretty(coalesce(sum(pg_column_size(outbound_body)),0)) AS outbound_bytes
  FROM public.session_bodies_2026_10 t
 WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00'
 GROUP BY 1 ORDER BY 1;

\echo '### W8. ★ 单会话内 outbound 随 turn_no 演化（★ 压缩塌缩点可见）'
-- ★ 期望看到：n_out / b_out 先单调增长 ⇒ 到某个 turn 突然塌缩
--   （压缩层把对话压回去）⇒ ⇒ 这正是「放大倍数不是严格 O(N²)」的机制。
WITH big AS (
  SELECT session_id, tenant_id, count(*) AS turns
    FROM public.session_bodies_2026_10 t
   WHERE ts >= '2026-10-15 00:00:00+00' AND ts < '2026-10-16 00:00:00+00'
   GROUP BY session_id, tenant_id
  HAVING count(*) > 100
   LIMIT 2
)
SELECT b.session_id, t.turn_no,
       CASE WHEN t.outbound_body IS NOT NULL AND t.outbound_body <> 'null'::jsonb
                 AND jsonb_typeof(t.outbound_body) = 'array'
            THEN jsonb_array_length(t.outbound_body) END AS n_out,
       pg_column_size(t.outbound_body) AS b_out,
       pg_column_size(t.request_delta) AS b_req,
       pg_column_size(t.response_delta) AS b_resp
  FROM public.session_bodies_2026_10 t
  JOIN big b ON b.session_id = t.session_id AND b.tenant_id = t.tenant_id
 WHERE t.ts >= '2026-10-15 00:00:00+00' AND t.ts < '2026-10-16 00:00:00+00'
 ORDER BY b.session_id, t.turn_no
 LIMIT 120;
